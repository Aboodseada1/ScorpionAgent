import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        ink: {
          50: "#f7f7f6",
          100: "#eeede9",
          200: "#d9d7d0",
          300: "#b9b5a9",
          400: "#8b867a",
          500: "#5f5c54",
          600: "#3e3c36",
          700: "#2a2925",
          800: "#1a1a17",
          900: "#0e0e0c",
        },
        paper: "#FAFAF7",
        paperDim: "#F2F1EC",
        card: "#FFFFFF",
        accent: "#2F6FED",
        accentDark: "#1F4FC4",
        ok: "#1FA971",
        warn: "#E8A33C",
        bad: "#D64545",
      },
      fontFamily: {
        sans: ["Inter", "ui-sans-serif", "system-ui"],
        display: ["Space Grotesk", "Inter", "system-ui"],
        mono: ["JetBrains Mono", "ui-monospace", "monospace"],
        /** Live call page — editorial + legible UI */
        call: ['"IBM Plex Sans"', "ui-sans-serif", "system-ui"],
        callSerif: ['"DM Serif Display"', "Georgia", "serif"],
      },
      boxShadow: {
        soft: "0 1px 3px rgba(10, 10, 10, 0.06), 0 8px 24px rgba(10, 10, 10, 0.04)",
        ring: "0 0 0 4px rgba(47,111,237,0.12)",
      },
      borderRadius: {
        "2xl": "1rem",
        "3xl": "1.5rem",
      },
    },
  },
  plugins: [],
} satisfies Config;
