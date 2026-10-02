import { useEffect } from "react";

declare global {
  interface Window {
    __reasonixFlushSessionDraft?: () => Promise<void>;
    __reasonixResumeSessionDraftEditing?: () => void;
  }
}

/** The shell's historical callback names now belong only to formal input. */
export function useSessionComposerLifecycle() {
  useEffect(() => {
    const flush = async () => {
      const input = await import("../lib/sessionComposerPersistence");
      try {
        await input.flushAllSessionComposers();
      } catch (error) {
        input.resumeSessionComposerEditing();
        throw error;
      }
    };
    const resume = () => {
      void import("../lib/sessionComposerPersistence").then(input => input.resumeSessionComposerEditing());
    };
    window.__reasonixFlushSessionDraft = flush;
    window.__reasonixResumeSessionDraftEditing = resume;
    return () => {
      if (window.__reasonixFlushSessionDraft === flush) delete window.__reasonixFlushSessionDraft;
      if (window.__reasonixResumeSessionDraftEditing === resume) delete window.__reasonixResumeSessionDraftEditing;
    };
  }, []);
}
