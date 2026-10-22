import React from 'react';
import antigravityIcon from './antigravity-icon.svg';
import claudeIcon from './claude-icon.svg';
import geminiIcon from './gemini-icon.svg';

// WorkBits: the pieces a work row is drawn with, the board's (Work.js)
// and the Overseer page's (OverseerWork.js) alike, so the two rows read
// the same.

// The engine that launched into the row's sandbox (stamped by the
// controller) — brand icons so an A/B board pair reads at a glance.
export const ENGINE_ICON = { antigravity: antigravityIcon, claude: claudeIcon, gemini: geminiIcon };
// size is the icon's in px; a smaller one (an inline row's) brings no
// margin of its own.
export function EngineIcon({ engine, title, size = 20 }) {
  const src = ENGINE_ICON[engine];
  if (!src) return null;
  return (
    <img src={src} alt={engine} title={title || `engine: ${engine}`}
      style={{ width: `${size}px`, height: `${size}px`, verticalAlign: 'middle', marginRight: size === 20 ? '6px' : 0 }} />
  );
}

// SessionMark ends a chip that opens a run's conversation: the icon of the
// agent it is with, or ↗ for an engine without one.
export function SessionMark({ engine }) {
  const src = ENGINE_ICON[engine];
  if (!src) return ' ↗';
  return (
    <img src={src} alt={engine}
      style={{ width: '14px', height: '14px', verticalAlign: '-2px', marginLeft: '4px' }} />
  );
}

// A chip says its status by colour alone, the word is in its hover:
// blue working, orange your move, green done, red failed, grey waiting.
export const RUN_STYLE = {
  running: { color: '#0969da', bg: 'rgba(9,105,218,0.12)' },
  starting: { color: '#0969da', bg: 'rgba(9,105,218,0.08)' },
  queued: { color: '#6a737d', bg: 'rgba(106,115,125,0.12)' },
  ready: { color: '#c2410c', bg: 'rgba(234,88,12,0.14)' },
  // A pending review on GitHub with no run left: finalize or discard it there.
  pending: { color: '#c2410c', bg: 'rgba(234,88,12,0.14)' },
  done: { color: '#22863a', bg: 'rgba(34,134,58,0.12)' },
  failed: { color: 'var(--danger, #d33)', bg: 'rgba(221,51,51,0.12)' },
};
// READY_STYLE is a draft's button: your verdict is the bottleneck.
export const READY_STYLE = RUN_STYLE.ready;

// recipeSource is where a run's recipe came from, for a hover: the
// built-in, or the repository's .agents/recipes file at the commit it was
// read at; '' for a run recorded before factory said.
export function recipeSource(run) {
  const source = run && run.source;
  if (!source) return '';
  if (source === 'builtin') return 'recipe: built-in';
  const [path, commit] = source.split('@');
  return `recipe: ${path}${commit ? ` @ ${commit.slice(0, 7)}` : ''}`;
}

// ageOf is how long ago ts was: 5m, 3h, 2d.
export function ageOf(ts) {
  if (!ts) return '';
  const mins = Math.floor((Date.now() - new Date(ts).getTime()) / 60000);
  if (mins < 60) return `${mins}m`;
  if (mins < 60 * 24) return `${Math.floor(mins / 60)}h`;
  return `${Math.floor(mins / (60 * 24))}d`;
}
