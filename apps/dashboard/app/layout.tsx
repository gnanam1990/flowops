import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { headers } from "next/headers";
import type { ReactNode } from "react";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

const title = "FlowOps — Economic control for autonomous agents";
const description =
  "Approve, pause, reconcile, and understand every agent payment on Base.";
const baseAppId = "6a8039cbe4a8a41598e7a325";

export async function generateMetadata(): Promise<Metadata> {
  const requestHeaders = await headers();
  const rawHost =
    requestHeaders.get("x-forwarded-host")?.split(",")[0]?.trim() ??
    requestHeaders.get("host")?.trim() ??
    "localhost:3000";
  const host = /^[a-z0-9.-]+(?::\d+)?$/i.test(rawHost)
    ? rawHost
    : "localhost:3000";
  const rawProtocol =
    requestHeaders.get("x-forwarded-proto")?.split(",")[0]?.trim() ?? "";
  const protocol =
    rawProtocol === "http" || rawProtocol === "https"
      ? rawProtocol
      : host.startsWith("localhost")
        ? "http"
        : "https";
  const origin = `${protocol}://${host}`;
  const socialImage = `${origin}/og.png`;

  return {
    title,
    description,
    other: {
      "base:app_id": baseAppId,
    },
    metadataBase: new URL(origin),
    openGraph: {
      title,
      description,
      type: "website",
      url: origin,
      images: [
        {
          url: socialImage,
          width: 1200,
          height: 630,
          alt: "FlowOps economic control room for autonomous agents on Base",
        },
      ],
    },
    twitter: {
      card: "summary_large_image",
      title,
      description,
      images: [socialImage],
    },
  };
}

export default function RootLayout({
  children,
}: Readonly<{
  children: ReactNode;
}>) {
  return (
    <html lang="en">
      <body className={`${geistSans.className} ${geistSans.variable} ${geistMono.variable}`}>
        {children}
      </body>
    </html>
  );
}
