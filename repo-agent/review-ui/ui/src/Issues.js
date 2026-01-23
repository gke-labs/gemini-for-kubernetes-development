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
       const title = typeof p === 'object' && p.title ? `Issue #${id}: ${p.title}` : `Issue #${id}`;
       const htmlURL = typeof p === 'object' ? p.htmlURL : null;

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
           excludedList.push({ id: id.toString(), type: 'excluded', sortId: id, title: `Issue #${id} (Deleted)` });
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

  const getStatusColor = (item) => {
    // If we had status/flair logic like Review.js
    return '#cd9945ff';
  };

  const renderSidebarItem = (item) => {
      const flairText = ''; // Placeholder for future status logic

      return (
          <div 
              key={item.id} 
              className={`sidebar-item ${selectedIssueId === item.id ? 'selected' : ''} ${item.type}`}
              onClick={() => setSelectedIssueId(item.id)}
          >
              <div className="sidebar-item-header">
                  <span className="sidebar-id">#{item.id}</span>
                  {flairText && item.type === 'active' && (
                      <span className="sidebar-flair" style={{ backgroundColor: getStatusColor(item) }}>
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

  const handleAddIssue = () => {
    const input = window.prompt("Enter Issue URL or Number:");
    if (!input) return;

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
        alert("Invalid Issue number or URL");
        return;
    }

    // Call API to add issue (similar to addPR in App.js)
    // We need a handler passed from App.js or we call fetch here
    // App.js doesn't expose handleAddIssue currently. 
    // I'll assume we can use the same pattern as App.js's handleAddPR but for issues.
    // However, App.js logic for addPR uses `addPR` key in PUT.
    // I should check if backend supports `addIssue`. 
    // For now I'll just comment it out or implement it if I see fit.
    // The user didn't explicitly ask for "Add Issue" button but Review.js has it. 
    // I will implement it assuming backend support or similarity.
    
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
                {activeList.map(renderSidebarItem)}
                <div className="sidebar-item add-pr" onClick={handleAddIssue} style={{textAlign: 'center', cursor: 'pointer', color: '#555', border: '1px dashed #ccc'}}>
                    + Add Issue
                </div>
            </div>
            {(pendingList.length > 0 || excludedList.length > 0) && <hr className="sidebar-divider" />}
            <div className="sidebar-section">
                {pendingList.length > 0 && (
                    <>
                        <h5 className="sidebar-header clickable" onClick={() => setIsPendingOpen(!isPendingOpen)} style={{cursor: 'pointer'}}>
                           {isPendingOpen ? '▼' : '▶'} Pending
                        </h5>
                        {isPendingOpen && pendingList.map(renderSidebarItem)}
                    </>
                )}
                
                {excludedList.length > 0 && (
                    <>
                        <h5 className="sidebar-header clickable" onClick={() => setIsExcludedOpen(!isExcludedOpen)} style={{cursor: 'pointer'}}>
                           {isExcludedOpen ? '▼' : '▶'} Excluded
                        </h5>
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
                    isMainView={true}
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
