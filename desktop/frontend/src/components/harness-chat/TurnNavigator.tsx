// Ported from DeepSeek Harness c291e7961a (MIT). The rail lists the complete
// conversation outline; a turn whose body is not loaded yet still navigates.
import {
  memo, useEffect, useId, useRef, useState,
  type CSSProperties, type MouseEvent, type PointerEvent,
} from 'react'
import type { ReactNode } from 'react'
import type { useT } from '../../lib/i18n'
/**
 * A mark either already has a mounted node — then `key` is the DOM anchor to
 * scroll to — or must page history in first.
 */
export type TurnRailAnchor = { kind: 'loaded'; key: string } | { kind: 'unloaded'; recordId: string; messageId?: string } | { kind: 'pending' }
/**
 * `turn` is the mark's stable identity and React key: it is the outline record
 * id, so it does not change when that turn finishes loading. `anchor` carries
 * where to scroll and how to resolve an unloaded target.
 */
export interface TurnRailItem { turn: string; ordinal: number; prompt: string; response: string; answerKey?: string; anchor: TurnRailAnchor; unloaded?: boolean }
import css from './TurnNavigator.styles'

interface TurnNavigatorProps {
  readonly items: readonly TurnRailItem[]
  readonly totalCount?: number
  readonly itemAt?: (index: number) => TurnRailItem
  readonly activeIndex?: number
  readonly onRange?: (first: number, end: number) => void
  readonly activeTurn: string | null
  /** Turn whose jump is still paging history in; its mark pulses. */
  readonly busyTurn: string | null
  readonly onNavigate: (item: TurnRailItem) => void
  readonly renderPreview: (item: TurnRailItem) => ReactNode
  readonly t: ReturnType<typeof useT>
  /**
   * The conversation outline is known to hold more than one turn. Keeps the
   * rail's area while the outline loads, so a returning reader does not see
   * navigation appear and disappear.
   */
  readonly loading?: boolean
  /** The outline could not be read; known markers stay and a retry is offered. */
  readonly failed?: boolean
  /** True when the offered retry re-runs a failed jump rather than the read. */
  readonly jumpFailed?: boolean
  /** Localized explanation of why the last jump failed. */
  readonly jumpReasonKey?: Parameters<ReturnType<typeof useT>>[0]
  /** The outline stopped short of the whole session; the rail says so. */
  readonly truncated?: boolean
  readonly onRetry?: () => void
  /** Present while a jump can still be abandoned. */
  readonly onCancelJump?: () => void
}

/** Fixed pitch between neighbouring marks; overflow scrolls inside the frame. */
const TURN_SPACING_PX = 10
/** Rail padding above the first mark and below the last one, per end. */
const RAIL_INSET_PX = 6
/** Fade band the mask reserves at a scrollable end. */
const FADE_PX = 24

type TurnPositionStyle = CSSProperties & {
  readonly '--turn-natural-position': string
}

type TurnFrameStyle = CSSProperties & {
  readonly '--turn-natural-height': string
  readonly '--turn-rail-inset': string
  readonly '--turn-scroll-top': string
}

function itemPosition(index: number): TurnPositionStyle {
  return { '--turn-natural-position': `${String(index * TURN_SPACING_PX)}px` }
}

function frameStyle(count: number, scrollTop: number): TurnFrameStyle {
  return {
    '--turn-natural-height': `${String((count - 1) * TURN_SPACING_PX + 2 * RAIL_INSET_PX)}px`,
    '--turn-rail-inset': `${String(RAIL_INSET_PX)}px`,
    '--turn-scroll-top': `${String(scrollTop)}px`,
  }
}

function itemAtPointer(
  count: number,
  frame: HTMLElement,
  scrollTop: number,
  clientY: number,
): number {
  const rect = frame.getBoundingClientRect()
  const offset = clientY - rect.top + scrollTop - RAIL_INSET_PX
  return Math.max(0, Math.min(count - 1, Math.round(offset / TURN_SPACING_PX)))
}

/** Scroll state the mask fades and follow logic read together. */
interface RailScrollState {
  readonly top: number
  readonly viewportHeight: number
}

const RAIL_AT_REST: RailScrollState = { top: 0, viewportHeight: 0 }

function railScrollState(scroller: HTMLElement, viewportHeight: number): RailScrollState {
  return { top: scroller.scrollTop, viewportHeight }
}

function sameRailScrollState(left: RailScrollState, right: RailScrollState): boolean {
  return left.top === right.top
    && left.viewportHeight === right.viewportHeight
}

function TurnNavigatorRail({ items, totalCount, itemAt, activeIndex, onRange, activeTurn, busyTurn, onNavigate, renderPreview, t, loading, failed, jumpFailed, jumpReasonKey, truncated, onRetry, onCancelJump }: TurnNavigatorProps) {
  const [previewIndex, setPreviewIndex] = useState(-1)
  const [scrollState, setScrollState] = useState<RailScrollState>(RAIL_AT_REST)
  const scrollerRef = useRef<HTMLDivElement | null>(null)
  /** While the pointer works the rail, follow must not move it under the hand. */
  const pointerInsideRef = useRef(false)
  const previewId = useId()
  const count = totalCount ?? items.length
  const getItem = itemAt ?? ((index: number) => items[index])
  const hasItems = count > 1
  const naturalHeight = Math.max(0, (count - 1) * TURN_SPACING_PX + 2 * RAIL_INSET_PX)
  const viewportHeight = scrollState.viewportHeight || Math.min(naturalHeight, 420)
  const firstVisible = Math.max(0, Math.floor((scrollState.top - RAIL_INSET_PX) / TURN_SPACING_PX) - 4)
  const lastVisible = Math.min(count, Math.ceil((scrollState.top + viewportHeight) / TURN_SPACING_PX) + 4)
  useEffect(() => { onRange?.(firstVisible, lastVisible) }, [onRange, firstVisible, lastVisible])

  const syncScrollState = (viewportHeight?: number): void => {
    const scroller = scrollerRef.current
    if (scroller === null) return
    setScrollState(current => {
      const next = railScrollState(scroller, viewportHeight ?? current.viewportHeight)
      return sameRailScrollState(current, next) ? current : next
    })
  }

  // Frame resizes (band/composer changes) move the overflow edges without a
  // scroll event; item count changes move the content height the same way.
  useEffect(() => {
    const scroller = scrollerRef.current
    if (scroller === null || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(entries => { syncScrollState(entries[0]?.contentRect.height ?? 0) })
    observer.observe(scroller)
    return () => { observer.disconnect() }
  }, [hasItems])

  // Keep the active mark visible: centre it whenever it leaves the scrollport,
  // unless the reader's pointer is working the rail.
  useEffect(() => {
    const scroller = scrollerRef.current
    const index = activeIndex ?? items.findIndex(item => item.anchor.kind === 'loaded' && item.anchor.key === activeTurn)
    if (scroller === null || index < 0 || pointerInsideRef.current) return
    const markTop = index * TURN_SPACING_PX + RAIL_INSET_PX
    const viewTop = scrollState.top
    const viewHeight = scrollState.viewportHeight
    if (viewHeight <= 0 || (markTop >= viewTop + FADE_PX && markTop <= viewTop + viewHeight - FADE_PX)) return
    const target = Math.max(0, markTop - viewHeight / 2)
    if (typeof scroller.scrollTo === 'function') {
      // A distant active turn must not animate through thousands of ranges
      // and fetch summaries for every intermediate rail viewport.
      scroller.scrollTo({ top: target, behavior: 'instant' })
    } else {
      scroller.scroll?.({ top: target })
    }
  }, [activeTurn, activeIndex, items, scrollState.top, scrollState.viewportHeight])

  // A known multi-turn conversation keeps its rail area while the outline is
  // still loading, and keeps the markers it already has after a failure.
  if (count < 2) {
    if (!loading && !failed) return null
    return (
      <div className={css.slot}>
        <nav className={css.frame} aria-label={t('chat.turnNavigation.label')} aria-busy={loading ? 'true' : undefined}
          style={frameStyle(Math.max(items.length, 2), 0)}>
          <div className={css.scroller}>
            <div className={css.marks}>
              {items.map((item, index) => (
                <div key={item.turn} className={css.markPosition} style={itemPosition(index)}>
                  <span className={css.mark} />
                </div>
              ))}
            </div>
          </div>
        </nav>
        {failed && onRetry !== undefined && (
          <button type="button" className="btn" onClick={onRetry}>{t('chat.turnNavigation.retry')}</button>
        )}
      </div>
    )
  }
  const preview = previewIndex < 0 || previewIndex >= count ? undefined : getItem(previewIndex)
  const previewPosition = previewIndex < 0 ? undefined : itemPosition(previewIndex)
  const previewAtPointer = (event: PointerEvent<HTMLElement>): void => {
    const scrollTop = scrollerRef.current?.scrollTop ?? 0
    const index = itemAtPointer(count, event.currentTarget, scrollTop, event.clientY)
    setPreviewIndex(index)
    onRange?.(index, index + 1)
  }
  const navigateAtPointer = (event: MouseEvent<HTMLElement>): void => {
    const scrollTop = scrollerRef.current?.scrollTop ?? 0
    const item = getItem(itemAtPointer(count, event.currentTarget, scrollTop, event.clientY))
    if (item !== undefined) onNavigate(item)
  }
  const fadeClasses = [css.scroller]
  if (scrollState.top > 1) fadeClasses.push(css.fadeTop)
  if (scrollState.top < naturalHeight - viewportHeight - 1) fadeClasses.push(css.fadeBottom)
  return (
    <div className={css.slot}>
      <nav
        className={css.frame}
        style={frameStyle(count, scrollState.top)}
        tabIndex={0}
        onKeyDown={event => {
          let next = previewIndex < 0 ? Math.max(0, activeIndex ?? 0) : previewIndex
          if (event.key === 'ArrowDown') next++
          else if (event.key === 'ArrowUp') next--
          else if (event.key === 'Home') next = 0
          else if (event.key === 'End') next = count - 1
          else if (event.key === 'Enter') { event.preventDefault(); onNavigate(getItem(next)); return }
          else return
          event.preventDefault()
          next = Math.max(0, Math.min(count - 1, next))
          pointerInsideRef.current = true
          setPreviewIndex(next); onRange?.(next, next + 1)
          scrollerRef.current?.scrollTo?.({ top: Math.max(0, next * TURN_SPACING_PX - viewportHeight / 2) })
        }}
        aria-label={t('chat.turnNavigation.label')}
        onClick={navigateAtPointer}
        onPointerMove={previewAtPointer}
        onPointerEnter={() => { pointerInsideRef.current = true }}
        onPointerLeave={() => {
          pointerInsideRef.current = false
          setPreviewIndex(-1)
        }}
      >
        <div
          ref={scrollerRef}
          className={fadeClasses.join(' ')}
          onScroll={() => { syncScrollState() }}
          data-nav-truncated={truncated ? 'true' : undefined}
          title={truncated ? t('chat.turnNavigation.truncated') : undefined}
        >
          <div className={css.marks}>
            {Array.from({ length: Math.max(0, lastVisible - firstVisible) }, (_, offset) => getItem(firstVisible + offset)).map((item, visibleIndex) => {
              const index = firstVisible + visibleIndex
              // The active mark is the mounted node, not the outline identity.
              const active = item.anchor.kind === 'loaded' && item.anchor.key === activeTurn
              const showingPreview = index === previewIndex
              const previewDistance = previewIndex < 0 ? -1 : Math.abs(index - previewIndex)
              const classes = [css.mark]

              if (active) classes.push(css.markActive)
              else if (showingPreview) classes.push(css.markPreview)
              if (item.turn === busyTurn) classes.push(css.markBusy)
              return (
                <div key={item.ordinal} className={css.markPosition} style={itemPosition(index)}>
                  <button
                    data-nav-turn={item.turn}
                    data-nav-unloaded={item.anchor.kind === 'unloaded' ? 'true' : undefined}
                    type="button"
                    className={classes.join(' ')}
                    aria-label={t(
                      'chat.turnNavigation.jump',
                      { turn: item.ordinal },
                    )}
                    aria-current={active ? 'true' : undefined}
                    aria-busy={item.turn === busyTurn ? 'true' : undefined}
                    data-preview-distance={previewDistance >= 0 && previewDistance <= 2 ? previewDistance : undefined}
                    aria-describedby={showingPreview ? previewId : undefined}
                    onClick={(event) => {
                      event.stopPropagation()
                      onNavigate(item)
                    }}
                    onFocus={() => { setPreviewIndex(index); onRange?.(index, index + 1) }}
                    onBlur={() => { setPreviewIndex(-1) }}
                  />
                </div>
              )
            })}
          </div>
        </div>
        {preview !== undefined && previewPosition !== undefined && (
          <div id={previewId} role="tooltip" className={css.preview} style={previewPosition}>
            {renderPreview(preview)}
          </div>
        )}
      </nav>
      {onRetry !== undefined && (
        <button type="button" className="btn chat-turn-navigation-retry" data-nav-retry={jumpFailed ? 'jump' : 'outline'}
          onClick={onRetry} title={jumpReasonKey === undefined ? undefined : t(jumpReasonKey)}>
          {t(jumpFailed ? 'chat.turnNavigation.retryJump' : 'chat.turnNavigation.retry')}
        </button>
      )}
      {onCancelJump !== undefined && (
        <button type="button" className="btn chat-turn-navigation-cancel" onClick={onCancelJump}>
          {t('chat.turnNavigation.cancel')}
        </button>
      )}
    </div>
  )
}

/**
 * Fixed-pitch rail of loaded turns with hover and focus previews. History is
 * loaded by the transcript's existing paging action. Overflow scrolls
 * inside the frame, gradient fades marking each scrollable end, and the
 * active mark keeps itself in view while the pointer is elsewhere.
 *
 * Memoized because it renders two host elements per Turn while the
 * enclosing view re-renders on every streaming delta: without the guard a long
 * session rebuilds hundreds of marks per commit for a rail that only changes
 * when a Turn is added, removed, or becomes active. Its props must therefore
 * stay referentially stable across those commits.
 */
export const TurnNavigator = memo(TurnNavigatorRail)
