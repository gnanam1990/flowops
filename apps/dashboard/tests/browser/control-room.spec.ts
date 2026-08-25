import { expect, test } from "@playwright/test";

test("renders truthful public state and an exact approval confirmation", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("banner").getByText("Live public status")).toBeVisible();
  await expect(page.getByRole("main").getByText("Base Sepolia (84532)", { exact: true })).toBeVisible();
  await expect(page.getByRole("main").getByText("Source verified").locator("..")).toContainText("Unavailable");

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
  await expect(dialog).toContainText("Base Sepolia (84532)");
  await expect(dialog).toContainText("0x1111111111111111111111111111111111111111");
  await expect(dialog).toContainText("0x036cbd53842c5426634e7929541ec2318f3dcf7e");
  await expect(dialog).toContainText(`0x${"a".repeat(64)}`);
  await expect(dialog.getByRole("button", { name: "Approve exact intent" })).toBeDisabled();

  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();

  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});
