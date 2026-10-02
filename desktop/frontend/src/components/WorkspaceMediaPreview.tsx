import { useEffect, useRef, type RefObject } from "react";
import type { FilePreview } from "../lib/types";
import { workspaceBasename } from "../lib/workspacePanelFormat";

export function WorkspaceMediaPreview({ preview }: { preview: FilePreview }) {
  if (!preview.url) return null;
  if (preview.kind === "image") {
    return (
      <div className="workspace-media workspace-media--image">
        <img src={preview.url} alt={workspaceBasename(preview.path)} decoding="async" draggable={false} />
      </div>
    );
  }
  if (preview.kind === "pdf") {
    return <iframe className="workspace-media workspace-media--pdf" src={preview.url} title={workspaceBasename(preview.path)} />;
  }
  if (preview.kind === "html") {
    return <iframe className="workspace-media workspace-media--html" src={preview.url} title={workspaceBasename(preview.path)} sandbox="allow-scripts" />;
  }
  if (preview.kind === "audio") {
    return <ReleasingMedia kind="audio" url={preview.url} />;
  }
  if (preview.kind === "video") {
    return <ReleasingMedia kind="video" url={preview.url} />;
  }
  return null;
}

export function releaseMediaElement(element: Pick<HTMLMediaElement, "pause" | "removeAttribute" | "load">): void {
  element.pause();
  element.removeAttribute("src");
  element.load();
}

function ReleasingMedia({ kind, url }: { kind: "audio" | "video"; url: string }) {
  const ref = useRef<HTMLMediaElement>(null);
  useEffect(() => () => { if (ref.current) releaseMediaElement(ref.current); }, [url]);
  const media = { src: url, controls: true, preload: "metadata" as const };
  return <div className={`workspace-media workspace-media--${kind}`}>
    {kind === "audio" ? <audio ref={ref as RefObject<HTMLAudioElement>} {...media} /> : <video ref={ref as RefObject<HTMLVideoElement>} {...media} />}
  </div>;
}
