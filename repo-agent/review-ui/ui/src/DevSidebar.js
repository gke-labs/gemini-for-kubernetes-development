import React, { useState } from 'react';

// --- Icons ---
const ChevronRight = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" style={{opacity: 0.8}}>
    <path fillRule="evenodd" d="M6.22 3.22a.75.75 0 0 1 1.06 0l4.25 4.25a.75.75 0 0 1 0 1.06l-4.25 4.25a.75.75 0 0 1-1.06-1.06L9.94 8 6.22 4.28a.75.75 0 0 1 0-1.06z"/>
  </svg>
);

const ChevronDown = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" style={{opacity: 0.8}}>
    <path fillRule="evenodd" d="M3.22 6.22a.75.75 0 0 1 1.06 0L8 9.94l3.72-3.72a.75.75 0 1 1 1.06 1.06l-4.25 4.25a.75.75 0 0 1-1.06 0l-4.25-4.25a.75.75 0 0 1 0-1.06z"/>
  </svg>
);

const FolderIcon = ({ open }) => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill={open ? "#54aeff" : "#8094a8"} style={{marginRight: '6px'}}>
    <path d="M1.75 1A1.75 1.75 0 0 0 0 2.75v10.5C0 14.216.784 15 1.75 15h12.5A1.75 1.75 0 0 0 16 13.25v-8.5A1.75 1.75 0 0 0 14.25 3H7.5a.25.25 0 0 1-.2-.1l-.9-1.2C6.07 1.26 5.55 1 5 1H1.75z"/>
  </svg>
);

const FileIcon = () => (
  <svg width="16" height="16" viewBox="0 0 16 16" fill="#6a737d" style={{marginRight: '6px'}}>
    <path d="M2 1.75C2 .784 2.784 0 3.75 0h6.586c.464 0 .909.184 1.237.513l2.914 2.914c.329.328.513.773.513 1.237v9.586A1.75 1.75 0 0 1 13.25 16h-9.5A1.75 1.75 0 0 1 2 14.25V1.75zm1.75-.25a.25.25 0 0 0-.25.25v12.5c0 .138.112.25.25.25h9.5a.25.25 0 0 0 .25-.25V6h-2.75A1.75 1.75 0 0 1 9 4.25V1.5H3.75z"/>
  </svg>
);

const PlusIcon = () => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor">
    <path d="M7.75 2a.75.75 0 0 1 .75.75V7h4.25a.75.75 0 0 1 0 1.5H8.5v4.25a.75.75 0 0 1-1.5 0V8.5H2.75a.75.75 0 0 1 0-1.5H7V2.75A.75.75 0 0 1 7.75 2z"/>
  </svg>
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
                            fontSize: '0.85em',
                            color: '#6a737d',
                            fontWeight: 'normal',
                            marginLeft: '8px',
                            fontFamily: 'monospace'
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
    activeRepo,
    activeCount,
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
            <div className="sidebar-header-row" style={{flexDirection: "column", alignItems: "flex-start"}}>
                <div style={{display: "flex", justifyContent: "space-between", width: "100%"}}>
                    <span className="sidebar-header-title">EXPLORATIONS</span>
                    <button 
                        className="sidebar-header-btn" 
                        onClick={onAddExploration} 
                        title="New Exploration"
                    >
                        <PlusIcon />
                    </button>
                </div>
                <div style={{fontSize: "0.8em", color: "#666", marginTop: "4px", paddingLeft: "10px"}}>
                    Active ({activeCount}/{activeRepo?.dev?.maxActiveSandboxes ?? '?'})
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
