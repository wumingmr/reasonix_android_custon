import assert from "node:assert/strict";
import { en } from "../locales/en";
import { zh } from "../locales/zh";
import { zhTW } from "../locales/zh-TW";

assert.equal(en["notice.deliveryIncompleteTitle"], "Checks are not complete");
assert.equal(en["notice.deliveryIncompleteBody"], "The response was generated, but required verification or review is still incomplete.");
assert.equal(en["notice.deliveryIncompleteContinue"], "Continue checks");
assert.equal(en["notice.deliveryRequirementTask"], "remaining implementation work");

assert.equal(zh["notice.deliveryIncompleteTitle"], "检查尚未完成");
assert.equal(zh["notice.deliveryIncompleteBody"], "内容已经生成，但必要的验证或复审仍未完成。");
assert.equal(zh["notice.deliveryIncompleteContinue"], "继续检查");
assert.equal(zh["notice.deliveryRequirementTask"], "剩余实施工作");

assert.equal(zhTW["notice.deliveryIncompleteTitle"], "檢查尚未完成");
assert.equal(zhTW["notice.deliveryIncompleteBody"], "內容已經產生，但必要的驗證或複審仍未完成。");
assert.equal(zhTW["notice.deliveryIncompleteContinue"], "繼續檢查");
assert.equal(zhTW["notice.deliveryRequirementTask"], "剩餘實作工作");

console.log("  PASS  final readiness recovery copy is aligned across locales");
