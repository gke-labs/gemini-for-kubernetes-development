import React, { useState } from 'react';

// --- Icons (Material Symbols) ---
const ChevronRight = () => (
  <span className="material-symbols-outlined" style={{fontSize: '18px', opacity: 0.5}}>chevron_right</span>
);

const ChevronDown = () => (
  <span className="material-symbols-outlined" style={{fontSize: '18px', opacity: 0.5}}>expand_more</span>
);

const FolderIcon = ({ open }) => (
  <span className="material-symbols-outlined" style={{fontSize: '18px', color: open ? 'var(--color-primary)' : 'rgba(234, 179, 8, 0.6)', marginRight: '6px'}}>folder</span>
);

const FileIcon = () => (
  <span className="material-symbols-outlined" style={{fontSize: '18px', color: 'rgba(96, 165, 250, 0.6)', marginRight: '6px'}}>description</span>
);

const PlusIcon = () => (
  <span className="material-symbols-outlined" style={{fontSize: '18px'}}>add</span>
);

// --- Tree Node ---
const TreeNode = ({ node, level, activeSandbox, onSelect, onAddApproach, ideaID }) => {
    const hasChildren = node.children && node.children.length > 0;
    const [expanded, setExpanded] = useState(true);

    const isActive = activeSandbox && activeSandbox.name === node.name;
    const indent = level * 14; // Compact indentation

    const handleExpandClick = (e) => {
        e.stopPropagation();
        setExpanded(!expanded);
    };

    const handleRowClick = (e) => {
        if (hasChildren) {
            // If it's a folder, toggle expand, but also select if it has data
            setExpanded(!expanded);
        }
        onSelect(node);
    };

    return (
        <React.Fragment>
            <div 
                className={`sidebar-tree-row ${isActive ? 'active' : ''}`}
                style={{ paddingLeft: `${10 + indent}px` }}
                onClick={handleRowClick}
            >
                {/* Expander Icon */}
                <span 
                    className="tree-expander"
                    onClick={handleExpandClick}
                    style={{visibility: hasChildren ? 'visible' : 'hidden'}}
                >
                    {expanded ? <ChevronDown /> : <ChevronRight />}
                </span>

                {/* Type Icon */}
                <span className="tree-icon">
                    {hasChildren ? <FolderIcon open={expanded} /> : <FileIcon />}
                </span>
                
                {/* Label */}
                <span className="tree-label" title={node.approach || node.branch}>
                    {node.approach || node.branch}
                </span>
            </div>
            
            {hasChildren && expanded && node.children.map(child => (
                <TreeNode 
                    key={child.name} 
                    node={child} 
                    level={level + 1} 
                    activeSandbox={activeSandbox} 
                    onSelect={onSelect}
                    onAddApproach={onAddApproach}
                    ideaID={ideaID}
                />
            ))}
        </React.Fragment>
    );
};

// --- Exploration Node (Root) ---
const ExplorationNode = ({ ideaID, description, isExpanded, onToggle, onAddApproach, children }) => {
    const [hover, setHover] = useState(false);

    return (
        <div className="tree-root-group">
            <div 
                className="sidebar-tree-row root-row"
                onClick={onToggle}
                onMouseEnter={() => setHover(true)}
                onMouseLeave={() => setHover(false)}
            >
                <span className="tree-expander">
                    {isExpanded ? <ChevronDown /> : <ChevronRight />}
                </span>
                <span className="tree-icon root-icon">
                    {isExpanded ? <FolderIcon open={true} /> : <FolderIcon open={false} />}
                </span>
                <span className="tree-label root-label">
                    {ideaID}
                    {description && (
                        <span style={{
                            fontSize: '11px',
                            color: 'var(--text-muted)',
                            fontWeight: 'normal',
                            marginLeft: '8px',
                            fontFamily: 'var(--font-mono)'
                        }}>
                            {description}
                        </span>
                    )}
                </span>
                
                {hover && (
                    <button 
                        className="tree-action-btn"
                        onClick={(e) => { e.stopPropagation(); onAddApproach(ideaID); }}
                        title="Add Child Approach"
                    >
                        <PlusIcon />
                    </button>
                )}
            </div>
            {children}
        </div>
    );
};

// --- Main Sidebar Component ---
function DevSidebar({
    explorations,
    ungrouped,
    activeSandbox,
    onSelectSandbox,
    onAddExploration,
    onAddApproach,
}) {
    const [expandedGroups, setExpandedGroups] = useState({});

    const toggleGroup = (ideaID) => {
        setExpandedGroups(prev => ({ ...prev, [ideaID]: !prev[ideaID] }));
    };

    // Helper to organize tree data
    const buildTree = (sandboxes) => {
        const byName = {};
        const roots = [];
        
        // 1. Initialize nodes
        sandboxes.forEach(s => {
            const name = s.approach || s.branch;
            if (!name) return;
            byName[name] = { ...s, children: [] };
        });

        // 2. Build Hierarchy
        sandboxes.forEach(s => {
            const name = s.approach || s.branch;
            const node = byName[name];
            if (!node) return;
            if (s.parentApproach && byName[s.parentApproach]) {
                byName[s.parentApproach].children.push(node);
            } else {
                roots.push(node);
            }
        });

        // 3. Sort
        const sortNodes = (nodes) => {
            nodes.sort((a, b) => {
                // Folders first
                const aIsFolder = a.children.length > 0;
                const bIsFolder = b.children.length > 0;
                if (aIsFolder && !bIsFolder) return -1;
                if (!aIsFolder && bIsFolder) return 1;
                
                const nameA = a.approach || a.branch;
                const nameB = b.approach || b.branch;
                return nameA.localeCompare(nameB);
            });
            nodes.forEach(n => sortNodes(n.children));
        };
        sortNodes(roots);

        return roots;
    };

    // Initialize all groups as expanded
    React.useEffect(() => {
        const initial = {};
        Object.keys(explorations).forEach(key => initial[key] = true);
        setExpandedGroups(prev => ({...initial, ...prev}));
    }, [explorations]);

    return (
        <div className="dev-sidebar">
            <div className="sidebar-header-row">
                <span className="sidebar-header-title">Explorations</span>
                <div style={{display: 'flex', gap: '4px'}}>
                    <button
                        className="sidebar-header-btn"
                        onClick={onAddExploration}
                        title="New Exploration"
                    >
                        <span className="material-symbols-outlined" style={{fontSize: '18px'}}>create_new_folder</span>
                    </button>
                </div>
            </div>
            
            <div className="sidebar-tree-content">
                {Object.keys(explorations).map(ideaID => {
                    const roots = buildTree(explorations[ideaID]);
                    const isExpanded = expandedGroups[ideaID];
                    const description = explorations[ideaID].find(s => s.description)?.description;

                    return (
                        <ExplorationNode
                            key={ideaID}
                            ideaID={ideaID}
                            description={description}
                            isExpanded={isExpanded}
                            onToggle={() => toggleGroup(ideaID)}
                            onAddApproach={onAddApproach}
                        >
                            {isExpanded && roots.map(root => (
                                <TreeNode 
                                    key={root.name}
                                    node={root}
                                    level={1} 
                                    activeSandbox={activeSandbox}
                                    onSelect={onSelectSandbox}
                                    onAddApproach={onAddApproach}
                                    ideaID={ideaID}
                                />
                            ))}
                        </ExplorationNode>
                    );
                })}
                
                {ungrouped.length > 0 && (
                    <div className="tree-root-group" style={{marginTop: '20px'}}>
                        <div className="sidebar-tree-row root-row" style={{cursor: 'default'}}>
                            <span className="tree-expander"></span>
                            <span className="tree-label root-label" style={{color: 'var(--text-secondary)'}}>MISC</span>
                        </div>
                        {ungrouped.map(sandbox => (
                             <div 
                                key={sandbox.name}
                                className={`sidebar-tree-row ${activeSandbox && activeSandbox.name === sandbox.name ? 'active' : ''}`}
                                style={{ paddingLeft: '24px' }}
                                onClick={() => onSelectSandbox(sandbox)}
                            >
                                <span className="tree-icon"><FileIcon /></span>
                                <span className="tree-label">{sandbox.branch}</span>
                            </div>
                        ))}
                    </div>
                )}
            </div>
        </div>
    );
}

export default DevSidebar;
