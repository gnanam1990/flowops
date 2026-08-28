import { expect, test } from "@playwright/test";

test("renders a concise mainnet public page and an exact approval confirmation", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Agent payments/ })).toBeVisible();
  await expect(page.getByLabel("Base mainnet activation status")).toContainText("4 deployed");
  await expect(page.getByLabel("Base mainnet activation status")).toContainText("Verified");
  await expect(page.getByText("Base mainnet · Chain ID 8453", { exact: true })).toBeVisible();
  await expect(page.getByText(/Sepolia/i)).toHaveCount(0);

  await page.goto("/api/local-auth/signin?return_to=%2F");
  await expect(page.getByRole("banner").getByText("Live control plane")).toBeVisible();
  const organizationContext = (page.viewportSize()?.width ?? 0) <= 900
    ? page.getByRole("banner").getByText(/Browser Operators/)
    : page.getByLabel("Primary navigation").getByText("Browser Operators");
  await expect(organizationContext).toBeVisible();
  await expect(page.getByText("1.250000 USDC").first()).toBeVisible();

  await page.getByRole("button", { name: /Buy verified browser dataset/ }).first().click();
  const dialog = page.getByRole("dialog", { name: "Buy verified browser dataset" });
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText("1,250,000 atomic");
  await expect(dialog).toContainText("Base Mainnet (8453)");
  await expect(dialog).toContainText("0x1111111111111111111111111111111111111111");
  await expect(dialog).toContainText("0x833589fcd6edb6e08f4c7c32d4f71b54bda02913");
  await expect(dialog).toContainText(`0x${"a".repeat(64)}`);
  await expect(dialog.getByRole("button", { name: "Approve exact intent" })).toBeDisabled();

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});

test("keeps the operational hierarchy intact at the tablet breakpoint", async ({ page }) => {
  await page.setViewportSize({ width: 800, height: 900 });
  await page.goto("/api/local-auth/signin?return_to=%2F");

  const geometry = await page.evaluate(() => {
    const hero = document.querySelector<HTMLElement>(".command-header");
    const actions = document.querySelector<HTMLElement>(".command-actions");
    const primaryValue = document.querySelector<HTMLElement>(".primary-balance > strong");
    const compactValue = document.querySelector<HTMLElement>(".balance-card.compact > strong");
    if (!hero || !actions || !primaryValue || !compactValue) throw new Error("control-room hierarchy is incomplete");
    const heroBox = hero.getBoundingClientRect();
    const actionBox = actions.getBoundingClientRect();
    return {
      actionFits: actionBox.left >= heroBox.left && actionBox.right <= heroBox.right + 1,
      primarySize: Number.parseFloat(getComputedStyle(primaryValue).fontSize),
      compactSize: Number.parseFloat(getComputedStyle(compactValue).fontSize),
    };
  });

  expect(geometry.actionFits).toBe(true);
  expect(geometry.primarySize).toBeGreaterThan(geometry.compactSize);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});
