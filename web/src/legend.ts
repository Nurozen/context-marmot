import { NODE_COLORS } from './types';

/**
 * Renders a legend panel in the #legend element showing node type colors,
 * edge style examples, and a size reference.
 */
export function renderLegend(): void {
  const el = document.getElementById('legend');
  if (!el) return;

  el.innerHTML = '';

  /* Node type color dots */
  for (const [type, color] of Object.entries(NODE_COLORS)) {
    const item = document.createElement('span');
    item.className = 'legend-item';

    const dot = document.createElement('span');
    dot.className = 'legend-dot';
    dot.style.backgroundColor = color;
    item.appendChild(dot);

    const label = document.createTextNode(type);
    item.appendChild(label);
    el.appendChild(item);
  }

  /* Edge style examples */
  const structuralItem = document.createElement('span');
  structuralItem.className = 'legend-item';
  structuralItem.innerHTML =
    `<svg width="24" height="8" style="flex-shrink:0">` +
    `<line x1="0" y1="4" x2="24" y2="4" stroke="rgba(114,183,178,0.6)" stroke-width="1.5"/></svg>` +
    `structural`;
  el.appendChild(structuralItem);

  const behavioralItem = document.createElement('span');
  behavioralItem.className = 'legend-item';
  behavioralItem.innerHTML =
    `<svg width="24" height="8" style="flex-shrink:0">` +
    `<line x1="0" y1="4" x2="24" y2="4" stroke="rgba(245,133,24,0.6)" stroke-width="1.5" stroke-dasharray="4 3"/></svg>` +
    `behavioral`;
  el.appendChild(behavioralItem);

  /* Size legend */
  const sizeSmall = document.createElement('span');
  sizeSmall.className = 'legend-item';
  sizeSmall.innerHTML =
    `<svg width="10" height="10" style="flex-shrink:0">` +
    `<circle cx="5" cy="5" r="4" fill="#999" opacity="0.6"/></svg>` +
    `few edges`;
  el.appendChild(sizeSmall);

  const sizeLarge = document.createElement('span');
  sizeLarge.className = 'legend-item';
  sizeLarge.innerHTML =
    `<svg width="16" height="16" style="flex-shrink:0">` +
    `<circle cx="8" cy="8" r="7" fill="#999" opacity="0.6"/></svg>` +
    `many edges`;
  el.appendChild(sizeLarge);
}
