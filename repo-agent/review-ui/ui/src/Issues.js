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
  handleScaleDown
}) {
  const [selectedIssueId, setSelectedIssueId] = useState(null);
  const [isPendingOpen, setIsPendingOpen] = useState(false);
  const [isExcludedOpen, setIsExcludedOpen] = useState(false);
  const [excludedDetails, setExcludedDetails] = useState({});
  const [issueTasks, setIssueTasks] = useState({});

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
    if (!text) return '#cd9945ff';
    const t = text.toLowerCase();
    if (t.includes('ready') || t.includes('completed') || t.includes('submitted')) return 'green';
    if (t.includes('running')) return 'orange';
    if (t.includes('failed')) return '#9e2a2aff';
    return '#cd9945ff';
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
                      <span className="sidebar-flair" style={{ backgroundColor: getStatusColor(flairText) }}>
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
    let issueNumber;
    
    if (issueId) {
        issueNumber = parseInt(issueId);
    } else {
        const input = window.prompt("Enter Issue URL or Number:");
        if (!input) return;

        issueNumber = parseInt(input);
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
    }

    if (isNaN(issueNumber) || !issueNumber) {
        alert("Invalid Issue number or URL");
        return;
    }
    
    fetch(`/api/repos/${activeRepo.name}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ addIssue: issueNumber })
    })
    .then(res => {
        if (res.ok) {
            alert(`Issue #${issueNumber} has been added to watch list.`);
            // Trigger refresh in App.js? App.js passes no refresh handler.
            // But App.js auto-refreshes.
        } else {
            res.json().then(data => alert("Failed to add Issue: " + data.error));
        }
    })
    .catch(err => console.error("Failed to add Issue:", err));
  };

  return (
    <div className="review-container">
        <div className="review-sidebar">
            <div className="sidebar-section">
                <h4 className="sidebar-header">
                    Active ({activeList.length}/{activeRepo.issue?.maxActiveSandboxes ?? '?'})
                </h4>
                {activeList.map(renderSidebarItem)}
                <div className="sidebar-item add-pr" onClick={() => handleAddIssue()} style={{textAlign: 'center', cursor: 'pointer', color: 'var(--text-secondary)', border: '1px dashed var(--border-color)'}}>
                    + Add Issue
                </div>
            </div>
            {(pendingList.length > 0 || excludedList.length > 0) && <hr className="sidebar-divider" />}
            <div className="sidebar-section">
                {pendingList.length > 0 && (
                    <>
                        <h4 className="sidebar-header clickable" onClick={() => setIsPendingOpen(!isPendingOpen)} style={{cursor: 'pointer'}}>
                           {isPendingOpen ? '▼' : '▶'} Pending
                        </h4>
                        {isPendingOpen && pendingList.map(renderSidebarItem)}
                    </>
                )}
                
                {excludedList.length > 0 && (
                    <>
                        <h4 className="sidebar-header clickable" onClick={() => setIsExcludedOpen(!isExcludedOpen)} style={{cursor: 'pointer'}}>
                           {isExcludedOpen ? '▼' : '▶'} Excluded
                        </h4>
                        {isExcludedOpen && excludedList.map(renderSidebarItem)}
                    </>
                )}
            </div>
        </div>
        <div className="review-main">
            {selectedIssue ? (
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
                />
            ) : (
                <div className="empty-state">
                    <p>Select an Issue to view details or add a new one.</p>
                </div>
            )}
        </div>
    </div>
  );
}

export default Issues;
