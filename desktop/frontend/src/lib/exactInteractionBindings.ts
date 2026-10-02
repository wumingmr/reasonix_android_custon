import type {
  ExtensionFormTarget,
  InteractionTargetView,
  PromptAnswerView,
} from "../generated/desktopContract.generated";

export interface ExactInteractionBindings {
  ResolvePromptForSession?(target: InteractionTargetView, answer: PromptAnswerView): Promise<void>;
  SubmitExtensionFormExact(target: ExtensionFormTarget, values: Record<string, unknown>): Promise<void>;
  ResolveRemoteTabPromptExact(target: InteractionTargetView, answer: PromptAnswerView): Promise<void>;
  SubmitRemoteTabExtensionFormExact(target: ExtensionFormTarget, values: Record<string, unknown>): Promise<void>;
}
