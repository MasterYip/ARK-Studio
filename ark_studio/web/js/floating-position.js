export function activationPoint(event) {
  if (event.detail > 0 || event.type.startsWith("pointer")) return { x: event.clientX, y: event.clientY };
  const rect = event.target?.getBoundingClientRect?.();
  return rect ? { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 } : null;
}

export function trackActivationPoint() {
  let point = null;
  const capture = (event) => { point = activationPoint(event) || point; };
  document.addEventListener("pointermove", capture, true);
  document.addEventListener("pointerdown", capture, true);
  document.addEventListener("click", capture, true);
  document.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") capture(event); }, true);
  return () => point && { ...point };
}

export function floatingPosition(anchor, width, height, viewportWidth, viewportHeight, topbar = 50) {
  const minX = 12, minY = topbar + 10;
  const maxX = Math.max(minX, viewportWidth - width - 12);
  const maxY = Math.max(minY, viewportHeight - height - 12);
  let x = viewportWidth - width - 16, y = topbar + 60;
  if (anchor && Number.isFinite(anchor.x) && Number.isFinite(anchor.y)) {
    x = anchor.x + 16;
    if (x > maxX && anchor.x - width - 16 >= minX) x = anchor.x - width - 16;
    y = anchor.y - 18;
  }
  return { x: Math.max(minX, Math.min(maxX, x)), y: Math.max(minY, Math.min(maxY, y)) };
}
