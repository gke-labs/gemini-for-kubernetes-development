import React, { useState, useEffect } from 'react';
import IssueCard from './IssueCard';

function Issues({
  activeRepo,
  issues,
  drafts,
  activeSubTab,
  handleIssueDraftChange,
  handleIssueSaveDraft,
  handleIssueSubmit,
  handleIssueDelete,
  getSandboxStatusClass,
  namespace,
  handleScaleUp,
  handleScaleDown,
  lastUpdated,
  showToast,
}) {
  const [selectedIssueId, setSelectedIssueId] = useState(null);
  const [isPendingOpen, setIsPendingOpen] = useState(false);
  const [isExcludedOpen, setIsExcludedOpen] = useState(false);
  const [excludedDetails, setExcludedDetails] = useState({});
  const [issueTasks, setIssueTasks] = useState({});
  const [addIssueModalOpen, setAddIssueModalOpen] = useState(false);
  const [addIssueInput, setAddIssueInput] = useState('');

  useEffect(() => {
    if (activeRepo && issues.length > 0) {
        issues.forEach(issue => {
            fetch(`/api/repo/${activeRepo.name}/issues/${issue.id}/tasks`)
                .then(res => res.json())
                .then(data => {
                    if (Array.isArray(data)) {
                        setIssueTasks(prev => ({ ...prev, [issue.id]: data }));
                    }
                })
                .catch(err => console.error("Failed to fetch tasks for sidebar:", err));
        });
    }
  }, [activeRepo, issues]);

  useEffect(() => {
    if (activeRepo) {
        // Fetch details for excluded issues
        if (activeRepo.excludeIssues) {
            activeRepo.excludeIssues.forEach(p => {
                const id = typeof p === 'object' ? p.id : p;
                if (!excludedDetails[id]) {
                    fetch(`/api/repo/${activeRepo.name}/issues/${id}/details`)
                        .then(res => {
                            if (res.ok) return res.json();
                            throw new Error("Failed");
                        })
                        .then(data => {
                            setExcludedDetails(prev => ({ ...prev, [id]: data }));
                        })
                        .catch(() => {});
                }
            });
        }
        
        // Fetch details for pending issues
        if (activeRepo.pendingIssues) {
            activeRepo.pendingIssues.forEach(p => {
                const id = typeof p === 'object' ? p.number : p;
                // Pending issues might come with title if cached, but if lazy we fetch
                // The backend now only returns number for pendingIssues
                if (!excludedDetails[id]) {
                     fetch(`/api/repo/${activeRepo.name}/issues/${id}/details`)
                        .then(res => {
                            if (res.ok) return res.json();
                            throw new Error("Failed");
                        })
                        .then(data => {
                            setExcludedDetails(prev => ({ ...prev, [id]: data }));
                        })
                        .catch(() => {});
                }
            });
        }
    }
  }, [activeRepo]);

  // 1. Active Issues
  const activeList = [];
  if (issues.length > 0) {
      issues.forEach(issue => activeList.push({ ...issue, id: String(issue.id), type: 'active', sortId: parseInt(issue.id) }));
  }

  // Sort active issues by ID descending (newest first)
  activeList.sort((a, b) => b.sortId - a.sortId);

  // 2. Pending Issues
  const pending = activeRepo.pendingIssues || [];
  let pendingList = [];
  pending.forEach(p => {
       // Handle both old format (number) and new format (object)
       const id = typeof p === 'object' ? p.number : p;
       
       const details = excludedDetails[id];
       // Prefer details from state (lazy fetch), fallback to object properties (if any), then generic
       let title = details && details.title ? `Issue #${id}: ${details.title}` : `Issue #${id}`;
       let htmlURL = details ? details.htmlURL : null;

       if (!details && typeof p === 'object' && p.title) {
           title = `Issue #${id}: ${p.title}`;
           htmlURL = p.htmlURL;
       }

       // Avoid duplicates if already in active
       if (!activeList.find(i => i.sortId === parseInt(id))) {
           pendingList.push({ id: id.toString(), type: 'pending', sortId: id, title: title, htmlURL: htmlURL });
       }
  });
  pendingList.sort((a, b) => b.sortId - a.sortId);
  // Limit to 10
  pendingList = pendingList.slice(0, 10);

  // 3. Excluded Issues
  const excluded = activeRepo.excludeIssues || [];
  const excludedList = [];
  excluded.forEach(p => {
       // excludeIssues might be numbers or objects depending on backend, assume numbers or check
       const id = typeof p === 'object' ? p.id : p; 
       if (!activeList.find(i => i.sortId === parseInt(id))) {
           const details = excludedDetails[id];
           const title = details && details.title ? `Issue #${id}: ${details.title}` : `Issue #${id} (Excluded)`;
           const htmlURL = details ? details.htmlURL : null;
           excludedList.push({ id: id.toString(), type: 'excluded', sortId: id, title: title, htmlURL: htmlURL });
       }
  });
  excludedList.sort((a, b) => b.sortId - a.sortId);

  // Auto-select first active issue if none selected
  useEffect(() => {
    if (!selectedIssueId && activeList.length > 0) {
        setSelectedIssueId(activeList[0].id);
    }
  }, [activeList, selectedIssueId]);

  const allItems = [...activeList, ...pendingList, ...excludedList];
  const selectedIssue = allItems.find(p => p.id === selectedIssueId);

  const getTaskStatus = (task) => {
    if (task.taskState === 'Completed') {
         if (task.result === 'submitted') return 'Submitted';
         return 'Ready';
    }
    if (task.taskState === 'Running') return 'Running';
    if (task.taskState === 'Failed') return 'Failed';
    return task.taskState || 'Pending';
  };

  const getIssueFlair = (issueId) => {
    const tasks = issueTasks[issueId];
    if (!tasks || tasks.length === 0) return null;
    
    // Sort tasks by creationTimestamp descending
    const sortedTasks = [...tasks].sort((a, b) => {
        return new Date(b.creationTimestamp) - new Date(a.creationTimestamp);
    });
    
    let targetTask = sortedTasks[0];
    const running = sortedTasks.find(t => t.taskState === 'Running');
    
    if (running) targetTask = running;
    
    if (!targetTask) return null;

    const status = getTaskStatus(targetTask);
    const name = targetTask.type.toUpperCase();
    return `${name}: ${status}`;
  };

  const getStatusColor = (text) => {
    if (!text) return '#f59e0b';
    const t = text.toLowerCase();
    if (t.includes('ready') || t.includes('completed') || t.includes('submitted')) return '#22c55e';
    if (t.includes('running')) return '#f59e0b';
    if (t.includes('failed')) return '#ef4444';
    return '#f59e0b';
  };

  const renderSidebarItem = (item) => {
      let flairText = '';
      if (item.type === 'active') {
          flairText = getIssueFlair(item.id);
      }

      return (
          <div 
              key={item.id} 
              className={`sidebar-item ${selectedIssueId === item.id ? 'selected' : ''} ${item.type}`}
              onClick={() => setSelectedIssueId(item.id)}
          >
              <div className="sidebar-item-header">
                  <span className="sidebar-id">#{item.id}</span>
                  {flairText && item.type === 'active' && (
                      <span className={`sidebar-flair ${flairText.toLowerCase().includes('running') ? 'status-badge-running' : ''}`} style={{ backgroundColor: getStatusColor(flairText) + '1a', color: getStatusColor(flairText), padding: '2px 6px', borderRadius: '4px', fontSize: '10px', fontWeight: '700', textTransform: 'uppercase' }}>
                          {flairText}
                      </span>
                  )}
              </div>
              <div className="sidebar-title" title={item.title}>
                  {item.title}
              </div>
              {item.type === 'pending' && <span className="sidebar-status">Pending</span>}
              {item.type === 'excluded' && <span className="sidebar-status">Excluded</span>}
          </div>
      );
  };

  const handleAddIssue = (issueId = null) => {
    if (issueId) {
        submitAddIssue(String(issueId));
    } else {
        setAddIssueInput('');
        setAddIssueModalOpen(true);
    }
  };

  const submitAddIssue = (input) => {
    let issueNumber = parseInt(input);
    if (isNaN(issueNumber)) {
        try {
          const url = new URL(input);
          const parts = url.pathname.split('/');
          const issuesIndex = parts.indexOf('issues');
          if (issuesIndex !== -1 && issuesIndex + 1 < parts.length) {
              issueNumber = parseInt(parts[issuesIndex + 1]);
          }
        } catch (e) {
          // ignore
        }
    }

    if (isNaN(issueNumber) || !issueNumber) {
        showToast("Invalid Issue number or URL", 'error');
        return;
    }

    fetch(`/api/repos/${activeRepo.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ addIssue: issueNumber })
    })
    .then(res => {
        if (res.ok) {
            showToast(`Issue #${issueNumber} has been added to watch list.`, 'success');
        } else {
            res.json().then(data => showToast("Failed to add Issue: " + data.error, 'error'));
        }
    })
    .catch(err => console.error("Failed to add Issue:", err));
  };

  return (
    <div className="review-container">
        <div className="review-sidebar">
            {/* Section header with count */}
            <div className="sidebar-section-header">
                <span className="sidebar-section-title">Open Issues</span>
                <span className="sidebar-section-count">{activeList.length}</span>
            </div>
            <div className="sidebar-section">
                {!lastUpdated && activeList.length === 0 && issues.length === 0 && (
                    <>
                        {[1,2,3].map(i => (
                            <div key={i} className="skeleton-sidebar-item" style={{animationDelay: `${i * 100}ms`}}>
                                <div className="skeleton skeleton-line short" style={{marginBottom: '8px'}}></div>
                                <div className="skeleton skeleton-line medium"></div>
                            </div>
                        ))}
                    </>
                )}
                {activeList.map(renderSidebarItem)}
                <div className="sidebar-item add-pr" onClick={() => handleAddIssue()} style={{textAlign: 'center', cursor: 'pointer', color: 'var(--color-primary)', border: '1px dashed var(--color-primary)', borderRadius: '8px', margin: '8px 12px', opacity: 0.7}}>
                    <span className="material-symbols-outlined" style={{fontSize: '16px', marginRight: '4px'}}>add</span> Add Issue
                </div>
            </div>
            <div className="sidebar-section">
                {pendingList.length > 0 && (
                    <>
                        <div className="sidebar-section-header" onClick={() => setIsPendingOpen(!isPendingOpen)} style={{cursor: 'pointer'}}>
                            <span style={{display: 'flex', alignItems: 'center', gap: '4px'}}>
                                <span className="material-symbols-outlined" style={{fontSize: '16px', color: 'var(--text-muted)'}}>{isPendingOpen ? 'expand_more' : 'chevron_right'}</span>
                                <span className="sidebar-section-title">Pending</span>
                            </span>
                            <span className="sidebar-section-count">{pendingList.length}</span>
                        </div>
                        {isPendingOpen && pendingList.map(renderSidebarItem)}
                    </>
                )}

                {excludedList.length > 0 && (
                    <>
                        <div className="sidebar-section-header" onClick={() => setIsExcludedOpen(!isExcludedOpen)} style={{cursor: 'pointer'}}>
                            <span style={{display: 'flex', alignItems: 'center', gap: '4px'}}>
                                <span className="material-symbols-outlined" style={{fontSize: '16px', color: 'var(--text-muted)'}}>{isExcludedOpen ? 'expand_more' : 'chevron_right'}</span>
                                <span className="sidebar-section-title">Excluded</span>
                            </span>
                            <span className="sidebar-section-count">{excludedList.length}</span>
                        </div>
                        {isExcludedOpen && excludedList.map(renderSidebarItem)}
                    </>
                )}
            </div>
        </div>
        <div className="review-main">
            {!lastUpdated && !selectedIssue && issues.length === 0 && activeList.length === 0 ? (
                <div style={{padding: '24px'}}>
                    {/* Skeleton: Issue header card */}
                    <div style={{border: '1px solid var(--border-color)', borderRadius: '12px', padding: '20px', marginBottom: '16px'}}>
                        <div style={{display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '16px'}}>
                            <div className="skeleton" style={{width: '60px', height: '16px', borderRadius: '4px'}}></div>
                            <div className="skeleton" style={{width: '200px', height: '16px', borderRadius: '4px'}}></div>
                            <div style={{marginLeft: 'auto'}} className="skeleton"><div style={{width: '80px', height: '24px', borderRadius: '4px'}}></div></div>
                        </div>
                        <div className="skeleton skeleton-line long" style={{marginBottom: '8px'}}></div>
                        <div className="skeleton skeleton-line medium"></div>
                    </div>
                    {/* Skeleton: Task card */}
                    <div style={{border: '1px solid var(--border-color)', borderRadius: '12px', padding: '20px', marginBottom: '16px'}}>
                        <div style={{display: 'flex', justifyContent: 'space-between', marginBottom: '16px'}}>
                            <div className="skeleton" style={{width: '120px', height: '14px', borderRadius: '4px'}}></div>
                            <div className="skeleton" style={{width: '60px', height: '14px', borderRadius: '4px'}}></div>
                        </div>
                        <div className="skeleton" style={{height: '80px', borderRadius: '8px'}}></div>
                    </div>
                </div>
            ) : selectedIssue ? (
                <IssueCard
                    key={selectedIssue.id}
                    issue={selectedIssue}
                    drafts={drafts}
                    activeSubTab={activeSubTab}
                    handleIssueDraftChange={handleIssueDraftChange}
                    handleIssueSaveDraft={handleIssueSaveDraft}
                    handleIssueSubmit={handleIssueSubmit}
                    handleIssueDelete={handleIssueDelete}
                    getSandboxStatusClass={getSandboxStatusClass}
                    namespace={namespace}
                    handleScaleUp={handleScaleUp}
                    handleScaleDown={handleScaleDown}
                    repoName={activeRepo.name}
                    handleAddIssue={handleAddIssue}
                    isMainView={true}
                    availableModels={activeRepo.issue?.models}
                    showToast={showToast}
                />
            ) : (
                <div className="empty-state">
                    <p>Select an Issue to view details or add a new one.</p>
                </div>
            )}
        </div>

        {/* Add Issue Modal */}
        {addIssueModalOpen && (
          <div className="modal-overlay" onClick={() => setAddIssueModalOpen(false)}>
            <div className="confirm-modal-content" onClick={(e) => e.stopPropagation()}>
              <h4 style={{margin: 0}}>Add Issue</h4>
              <p>Enter an Issue URL or number:</p>
              <input
                type="text"
                value={addIssueInput}
                onChange={(e) => setAddIssueInput(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') { setAddIssueModalOpen(false); submitAddIssue(addIssueInput); } }}
                placeholder="e.g. 42 or https://github.com/owner/repo/issues/42"
                autoFocus
                style={{padding: '12px', border: '1px solid var(--border-color-input)', borderRadius: '8px', backgroundColor: 'var(--bg-input)', color: 'var(--text-primary)', fontSize: '14px', fontFamily: 'var(--font-ui)'}}
              />
              <div className="confirm-modal-actions">
                <button className="btn btn-secondary" onClick={() => setAddIssueModalOpen(false)}>Cancel</button>
                <button className="btn btn-submit" onClick={() => { setAddIssueModalOpen(false); submitAddIssue(addIssueInput); }}>Add</button>
              </div>
            </div>
          </div>
        )}
    </div>
  );
}

export default Issues;
