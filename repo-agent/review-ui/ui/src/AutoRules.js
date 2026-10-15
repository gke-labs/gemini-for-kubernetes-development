import React from 'react';

// The board's automation rules (spec.auto.rules), edited in its settings:
// each a recipe of the catalog, the item type it runs on, the condition an
// item must meet and an optional daily cap. A rule for a recipe that
// writes to GitHub when it finishes says so.

// CONDITIONS are the conditions a rule's row offers, one each; a rule
// setting several (written by hand) shows as "custom" and is kept as it is.
export const CONDITIONS = [
  { value: 'all', label: 'every open item', when: () => ({}) },
  { value: 'unclaimed', label: 'unclaimed (no assignees)', when: () => ({ unclaimed: true }) },
  { value: 'assigned', label: 'assigned to me', when: () => ({ assignedToOwner: true }) },
  { value: 'requested', label: 'requesting my review', when: () => ({ reviewRequestedFromOwner: true }), on: 'pr' },
  { value: 'labels', label: 'labelled…', when: labels => ({ labels }) },
];

// conditionOf is the one condition a rule's when sets, or 'custom'.
export function conditionOf(when = {}) {
  const set = [
    when.unclaimed && 'unclaimed',
    when.assignedToOwner && 'assigned',
    when.reviewRequestedFromOwner && 'requested',
    (when.labels || []).length > 0 && 'labels',
  ].filter(Boolean);
  if (set.length === 0) return 'all';
  return set.length === 1 ? set[0] : 'custom';
}

// itemTypes are the item types a rule for rec may run on: the ones it runs
// on, of issue and pr. A my-pr recipe runs only on its member's PRs, and a
// rule's PRs are anyone's, so it has none.
export function itemTypes(rec) {
  const on = (rec && rec.on) || [];
  if (on.length === 0) return ['issue', 'pr'];
  return ['issue', 'pr'].filter(t => on.includes(t));
}

export default function AutoRules({ rules = [], recipes = [], errors = [], onChange }) {
  const automatable = recipes.filter(r => itemTypes(r).length > 0);
  const recipeOf = name => recipes.find(r => r.name === name);
  const update = (i, rule) => onChange(rules.map((r, j) => (j === i ? rule : r)));
  const remove = i => onChange(rules.filter((_, j) => j !== i));
  const add = () => {
    const rec = automatable[0];
    if (!rec) return;
    onChange([...rules, { recipe: rec.name, on: itemTypes(rec)[0], when: {} }]);
  };

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', fontSize: 'small' }}>
      {rules.length === 0 && <div style={{ color: 'var(--text-secondary)' }}>No rules: nothing runs without a click.</div>}
      {rules.map((rule, i) => {
        const rec = recipeOf(rule.recipe);
        const types = rec ? itemTypes(rec) : [rule.on];
        const condition = conditionOf(rule.when);
        const setRecipe = name => {
          const next = recipeOf(name);
          const on = itemTypes(next).includes(rule.on) ? rule.on : itemTypes(next)[0];
          update(i, { ...rule, recipe: name, on });
        };
        const setOn = on => {
          // An issue has no review requests.
          const when = on === 'issue' && rule.when && rule.when.reviewRequestedFromOwner ? {} : rule.when;
          update(i, { ...rule, on, when });
        };
        const setCondition = value => {
          const c = CONDITIONS.find(x => x.value === value);
          update(i, { ...rule, when: c.when((rule.when && rule.when.labels) || []) });
        };
        return (
          <div key={i} data-testid="auto-rule" style={{ display: 'flex', gap: '6px', alignItems: 'center', flexWrap: 'wrap' }}>
            <select aria-label="Recipe" value={rule.recipe} onChange={e => setRecipe(e.target.value)}>
              {!rec && <option value={rule.recipe}>{rule.recipe} (not in the catalog)</option>}
              {automatable.map(r => <option key={r.name} value={r.name}>{r.label || r.name}</option>)}
            </select>
            on
            <select aria-label="Item type" value={rule.on} onChange={e => setOn(e.target.value)}>
              {types.map(t => <option key={t} value={t}>{t === 'pr' ? 'PRs' : 'issues'}</option>)}
            </select>
            <select aria-label="Condition" value={condition} onChange={e => setCondition(e.target.value)}>
              {condition === 'custom' && <option value="custom">custom</option>}
              {CONDITIONS.filter(c => !c.on || c.on === rule.on).map(c => <option key={c.value} value={c.value}>{c.label}</option>)}
            </select>
            {condition === 'labels' && (
              <input aria-label="Labels" type="text" placeholder="label, label" style={{ width: '110px' }}
                value={(rule.when.labels || []).join(', ')}
                onChange={e => update(i, { ...rule, when: { ...rule.when, labels: e.target.value.split(',').map(v => v.trim()) } })} />
            )}
            <input aria-label="Per day" type="number" min="0" placeholder="no cap" title="At most this many a day; empty: no cap"
              style={{ width: '64px' }} value={rule.maxPerDay || ''}
              onChange={e => update(i, { ...rule, maxPerDay: parseInt(e.target.value, 10) || 0 })} />
            /day
            <button type="button" className="btn btn-sm" aria-label="Remove rule" onClick={() => remove(i)}>✕</button>
            {rec && rec.onFinish && (
              <span style={{ flexBasis: '100%', color: 'var(--text-secondary)' }}>
                {rec.label || rec.name} applies {rec.onFinish} when it finishes, without a click.
              </span>
            )}
          </div>
        );
      })}
      {errors.map(e => <div key={e} className="warning-banner">Skipped: {e}</div>)}
      <div>
        <button type="button" className="btn btn-sm" onClick={add} disabled={automatable.length === 0}>+ Add rule</button>
      </div>
    </div>
  );
}
