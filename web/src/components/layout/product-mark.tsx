/*
 * Open MCP Portal's Keystone mark (an arch whose teal keystone holds it together), shown where an instance has no logo of its own
 * (UI_LOGO_URL). It is the exact brand file from public/, not theme colours, so a theme never recolours the product's logo.
 * Always the tile mark: it reads on the light and the dark theme (the bare mark's teal keystone doesn't on light pages).
 */
export function ProductMark({ className, small }: { className?: string; small?: boolean }) {
  // small: 20 to 47 px: the sidebar (20 px), the sign-in and session pages (40 px). Otherwise the regular drawing, for 48 px and up.
  return <img src={small ? "/open-mcp-portal-mark-small.svg" : "/open-mcp-portal-mark.svg"} alt="" className={className} />;
}
