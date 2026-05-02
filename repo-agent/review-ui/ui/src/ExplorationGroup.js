// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
    repoName
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
        <div className="exploration-group" style={{ marginBottom: '20px', border: '1px solid var(--border-color)', borderRadius: '8px', backgroundColor: 'var(--bg-card)', boxShadow: 'var(--shadow-card)' }}>
            <div 
                className="exploration-header" 
                style={{ 
                    padding: '15px', 
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
                    <span style={{ fontSize: '1.2rem', fontWeight: 'bold' }}>📂 {ideaID}</span>
                    <span style={{ fontSize: '0.9rem', color: 'var(--text-secondary)', backgroundColor: 'var(--bg-secondary)', padding: '2px 8px', borderRadius: '12px' }}>
                        {sandboxes.length} approach{sandboxes.length !== 1 ? 'es' : ''}
                    </span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '10px' }}>
                     <button 
                        className="btn" 
                        onClick={(e) => { 
                            e.stopPropagation(); 
                            onAddApproach(ideaID); 
                        }} 
                        title="Add new approach"
                        style={{ fontSize: '1.2rem', width: '36px', height: '36px', borderRadius: '18px', padding: 0, display: 'flex', alignItems: 'center', justifyContent: 'center' }}
                    >
                        +
                    </button>
                    <span style={{ color: 'var(--text-secondary)' }}>
                        {isExpanded ? '▲' : '▼'}
                    </span>
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
                            />
                        </div>
                    ))}
                </div>
            )}
        </div>
    );
}

export default ExplorationGroup;