// Built for the page realm separately: bundler helpers remain inside the
// closure when the complete resource is sent to a website.
export { pageSemanticSnapshot as pageSnapshot, pageQuery, pageReady } from "./pageSemantic.js";
export { pageResolve, pageSelect, pageFocus, pageIdentity, pageLocate, pageFrameGeometry } from "./pageActions.js";
export { pageInputReady, pageInputFrame } from "./pageInput.js";
export { pagePickElement, pageCancelPicker } from "./pagePicker.js";
