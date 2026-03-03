import React, { useState } from 'react';
import DevCard from './DevCard';

function ExplorationGroup({
    ideaID,
    sandboxes,
    onAddApproach,
    onDeleteApproach,
    onScaleUp,
    onScaleDown,
    onFork,
    namespace,
    getSandboxStatusClass,
    repoName,
    showToast,
}) {
    const [isExpanded, setIsExpanded] = useState(true);

    // Organize sandboxes into a visual tree hierarchy (flat list with levels)
    const organizedSandboxes = React.useMemo(() => {
        const byName = {};
        const roots = [];
        
        // 1. Index by approach (or branch name if approach missing)
        sandboxes.forEach(s => {
            const name = s.approach || s.branch;
            // Create a wrapper object for tree building
            byName[name] = { ...s, children: [], original: s };
        });

        // 2. Build Tree
        sandboxes.forEach(s => {
            const name = s.approach || s.branch;
            const node = byName[name];
            
            if (s.parentApproach && byName[s.parentApproach]) {
                byName[s.parentApproach].children.push(node);
            } else {
                roots.push(node);
            }
        });

        // 3. Flatten DFS
        const flattened = [];
        const traverse = (nodes, level) => {
            // Sort by creation or name? Name is usually sequential enough (v1, v2)
            nodes.sort((a, b) => a.name.localeCompare(b.name));
            
            nodes.forEach(node => {
                flattened.push({ ...node.original, level });
                traverse(node.children, level + 1);
            });
        };
        traverse(roots, 0);
        return flattened;
    }, [sandboxes]);

    return (
        <div className="exploration-group" style={{ marginBottom: '20px', border: '1px solid var(--border-color)', borderRadius: '12px', backgroundColor: 'var(--bg-card)', boxShadow: 'var(--shadow-card)', overflow: 'hidden' }}>
            <div
                className="exploration-header"
                style={{
                    padding: '12px 16px',
                    borderBottom: isExpanded ? '1px solid var(--border-color)' : 'none',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    cursor: 'pointer',
                    backgroundColor: 'var(--bg-header)'
                }}
                onClick={() => setIsExpanded(!isExpanded)}
            >
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                    <span className="material-symbols-outlined" style={{fontSize: '18px', color: 'var(--text-muted)'}}>{isExpanded ? 'expand_more' : 'chevron_right'}</span>
                    <span className="material-symbols-outlined" style={{fontSize: '20px', color: isExpanded ? 'var(--color-primary)' : 'rgba(234, 179, 8, 0.6)'}}>folder</span>
                    <span style={{ fontSize: '14px', fontWeight: 700 }}>{ideaID}</span>
                    <span className="sidebar-section-count">
                        {sandboxes.length}
                    </span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                     <button
                        className="sidebar-header-btn"
                        onClick={(e) => {
                            e.stopPropagation();
                            onAddApproach(ideaID);
                        }}
                        title="Add new approach"
                    >
                        <span className="material-symbols-outlined" style={{fontSize: '18px'}}>add</span>
                    </button>
                </div>
            </div>
            
            {isExpanded && (
                <div className="exploration-content" style={{ padding: '15px', backgroundColor: 'var(--bg-secondary)' }}>
                    {organizedSandboxes.map(sandbox => (
                        <div key={sandbox.name} style={{ marginBottom: '10px', marginLeft: `${sandbox.level * 30}px`, borderLeft: sandbox.level > 0 ? '2px solid var(--border-color)' : 'none', paddingLeft: sandbox.level > 0 ? '10px' : '0' }}>
                            <div style={{ marginBottom: '5px', fontSize: '0.9rem', color: 'var(--text-secondary)', marginLeft: '5px' }}>
                                {sandbox.level > 0 && <span style={{marginRight: '5px', color: 'var(--text-muted)'}}>↳</span>}
                                {sandbox.approach ? (
                                    <strong>Approach: {sandbox.approach}</strong>
                                ) : (
                                    <span>Branch: {sandbox.branch}</span>
                                )}
                            </div>
                            <DevCard
                                sandbox={sandbox}
                                handleDelete={onDeleteApproach}
                                getSandboxStatusClass={getSandboxStatusClass}
                                namespace={namespace}
                                handleScaleUp={onScaleUp}
                                handleScaleDown={onScaleDown}
                                handleFork={onFork}
                                repoName={repoName}
                                showToast={showToast}
                            />
                        </div>
                    ))}
                </div>
            )}
        </div>
    );
}

export default ExplorationGroup;