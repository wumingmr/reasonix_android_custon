import { useCallback, useRef, useState } from "react";

/** Stop captures its source synchronously and has a lock independent of answers. */
export function usePromptStop(onStop: () => void | Promise<void>) {
  const pending = useRef(false);
  const [stopping, setStopping] = useState(false);
  const [stopFailed, setStopFailed] = useState(false);
  const stopTask = useCallback(() => {
    if (pending.current) return;
    pending.current = true;
    setStopping(true);
    setStopFailed(false);
    const failed = () => {
      pending.current = false;
      setStopping(false);
      setStopFailed(true);
    };
    try {
      // Calling before the first await keeps committed commands on the tab
      // whose Stop button was clicked, even if navigation happens next.
      void Promise.resolve(onStop()).catch(failed);
    } catch {
      failed();
    }
  }, [onStop]);
  return { stopping, stopFailed, stopTask };
}
