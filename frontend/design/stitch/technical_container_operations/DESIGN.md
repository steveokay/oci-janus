---
name: Technical Container Operations
colors:
  surface: '#f8f9ff'
  surface-dim: '#cbdbf5'
  surface-bright: '#f8f9ff'
  surface-container-lowest: '#ffffff'
  surface-container-low: '#eff4ff'
  surface-container: '#e5eeff'
  surface-container-high: '#dce9ff'
  surface-container-highest: '#d3e4fe'
  on-surface: '#0b1c30'
  on-surface-variant: '#44474d'
  inverse-surface: '#213145'
  inverse-on-surface: '#eaf1ff'
  outline: '#74777d'
  outline-variant: '#c4c6cd'
  surface-tint: '#4d6079'
  primary: '#000917'
  on-primary: '#ffffff'
  primary-container: '#0d2137'
  on-primary-container: '#7689a4'
  inverse-primary: '#b5c8e5'
  secondary: '#2f6096'
  on-secondary: '#ffffff'
  secondary-container: '#97c4ff'
  on-secondary-container: '#1b5185'
  tertiary: '#000a03'
  on-tertiary: '#ffffff'
  tertiary-container: '#002610'
  on-tertiary-container: '#009c54'
  error: '#ba1a1a'
  on-error: '#ffffff'
  error-container: '#ffdad6'
  on-error-container: '#93000a'
  primary-fixed: '#d2e4ff'
  primary-fixed-dim: '#b5c8e5'
  on-primary-fixed: '#081c32'
  on-primary-fixed-variant: '#364860'
  secondary-fixed: '#d2e4ff'
  secondary-fixed-dim: '#a1c9ff'
  on-secondary-fixed: '#001c37'
  on-secondary-fixed-variant: '#0d487c'
  tertiary-fixed: '#65fe9f'
  tertiary-fixed-dim: '#43e186'
  on-tertiary-fixed: '#00210d'
  on-tertiary-fixed-variant: '#00522a'
  background: '#f8f9ff'
  on-background: '#0b1c30'
  surface-variant: '#d3e4fe'
typography:
  display:
    fontFamily: Hanken Grotesk
    fontSize: 36px
    fontWeight: '700'
    lineHeight: 44px
    letterSpacing: -0.02em
  headline-lg:
    fontFamily: Hanken Grotesk
    fontSize: 28px
    fontWeight: '600'
    lineHeight: 36px
  headline-md:
    fontFamily: Hanken Grotesk
    fontSize: 20px
    fontWeight: '600'
    lineHeight: 28px
  body-lg:
    fontFamily: Hanken Grotesk
    fontSize: 16px
    fontWeight: '400'
    lineHeight: 24px
  body-md:
    fontFamily: Hanken Grotesk
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  code-md:
    fontFamily: JetBrains Mono
    fontSize: 14px
    fontWeight: '400'
    lineHeight: 20px
  code-sm:
    fontFamily: JetBrains Mono
    fontSize: 12px
    fontWeight: '500'
    lineHeight: 16px
  label-caps:
    fontFamily: Hanken Grotesk
    fontSize: 12px
    fontWeight: '700'
    lineHeight: 16px
    letterSpacing: 0.05em
rounded:
  sm: 0.125rem
  DEFAULT: 0.25rem
  md: 0.375rem
  lg: 0.5rem
  xl: 0.75rem
  full: 9999px
spacing:
  base: 4px
  xs: 4px
  sm: 8px
  md: 16px
  lg: 24px
  xl: 32px
  container-max: 1440px
  gutter: 20px
---

## Brand & Style
The design system is engineered for developers and DevOps engineers who prioritize precision, security, and speed. The brand personality is technical, reliable, and utilitarian, evoking the feeling of a well-organized terminal or a high-performance workstation.

The aesthetic follows a **Modern Corporate/Technical** style with a focus on high data density and logical hierarchy. It avoids unnecessary decoration, opting instead for structural clarity and crisp information architecture. The goal is to build trust through transparency, using clear status indicators and robust documentation patterns.

## Colors
This design system utilizes a palette of deep blues and slates to convey authority and stability.
- **Primary (Deep Navy):** Used for navigation, headers, and primary actions to anchor the UI.
- **Secondary (Slate Blue):** Used for interactive elements and supportive UI components.
- **Tertiary (Security Green):** Dedicated to "Success" states and clean vulnerability scans.
- **Neutrals:** A range of slates (Slate-50 to Slate-900) manages the hierarchy of secondary text and structural borders.
- **Semantic Accents:** Amber (#F59E0B) for warnings and Crimson (#EF4444) for high-risk vulnerabilities or build failures.

## Typography
The system uses **Hanken Grotesk** for the interface to ensure a modern, clean, and highly legible experience. Its sharp terminals and contemporary proportions align with the "dev-focused" narrative.

For technical data—including SHA hashes, CLI commands, tags, and version numbers—**JetBrains Mono** is used exclusively. This monospaced font provides the necessary character distinction (like 0 vs O) critical for technical accuracy. Labels for metadata should often be set in `label-caps` to distinguish them from interactive content.

## Layout & Spacing
This design system employs a **Fixed Grid** approach for desktop views to maintain control over information density, centered within a 1440px container.

- **Grid:** 12-column layout with 20px gutters.
- **Density:** High. Vertical spacing is tight (8px or 16px between related items) to allow users to scan large volumes of container tags and security data without excessive scrolling.
- **Responsive:** On mobile, the grid collapses to 4 columns with 16px margins; however, technical data tables should support horizontal scrolling to preserve column integrity.

## Elevation & Depth
Depth is created primarily through **Tonal Layers** and **Low-contrast Outlines** rather than heavy shadows. This reinforces the clean, "flat" technical aesthetic.

- **Surface 0 (Background):** Slate-50 (#F8FAFC) used for the main application canvas.
- **Surface 1 (Card/Container):** White (#FFFFFF) with a 1px border (#E2E8F0).
- **Surface 2 (Hover/Active):** Slate-100 (#F1F5F9) for subtle interaction feedback.
- **Shadows:** Only used for floating menus or modals, employing a "Soft Industrial" shadow: `0 4px 6px -1px rgb(0 0 0 / 0.1), 0 2px 4px -2px rgb(0 0 0 / 0.1)`.

## Shapes
The design system uses a **Soft (0.25rem)** roundedness level. This provides a subtle modern touch while maintaining the structural, "block-based" feel of a technical platform. 
- Standard buttons and inputs: 4px radius.
- Large containers and cards: 8px radius.
- Code blocks: 6px radius.
- Status badges: 2px (near-sharp) to differentiate them from interactive buttons.

## Components
- **Buttons:** Primary buttons are Solid Navy (#0D2137) with white text. Secondary buttons use the Slate border/White fill "ghost" style.
- **Status Badges:** Critical for vulnerability reporting. Use small, rectangular badges with low-opacity backgrounds and high-contrast text (e.g., "Critical" has a light red background and dark red text). Include a small icon for accessibility.
- **Code Blocks:** Dark-themed blocks (#1E293B) for `docker pull` commands. Use a "Copy to Clipboard" icon button in the top-right corner.
- **Data Tables:** These are the heart of the UI. Use a fixed-header style with subtle zebra-striping. Column headers should be `label-caps`. 
- **Tags:** Image tags should be styled as "Chips" using the Monospace font, providing a distinct visual contrast to standard UI text.
- **Progress Indicators:** Use thin, 4px height linear bars for build progress or scan completion to maintain a minimalist profile.