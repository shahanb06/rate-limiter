import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Rate Limiter Dashboard",
  description: "Live analytics for the rate-limiter service",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen antialiased">{children}</body>
    </html>
  );
}
