import type { PluggableList } from "unified";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";
import { remarkMathPolicy } from "./remarkMathPolicy";
import { remarkLocalPathLinks } from "../lib/localPathLinks";

// One shared parser policy keeps live Markdown and session exports identical.
// singleTilde is off because "500~1000" is a range; only ~~ strikes through.
export const reasonixRemarkPlugins = [[remarkGfm, { singleTilde: false }], remarkMath, remarkMathPolicy, remarkLocalPathLinks] satisfies PluggableList;
export { reasonixRehypePlugins } from "./rehypeReasonixKatex";
