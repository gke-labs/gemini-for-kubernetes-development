import React, { useState, useEffect } from 'react';
import PrReviewCard from './PrReviewCard';

function Review({
  activeRepo,
  prs,
  drafts,
  collapsedReviews,
  reviewViewModes,
  yamlDrafts,
  handleDelete,
  handleSaveDraft,
  handleDraftChange,
  handleRemoveComment,
  toggleReviewView,
  handleYamlDraftChange,
  handleYamlDraftBlur,
  handleSubmit,
  handleExportCurl,
  getSandboxStatusClass,
  toggleCollapse,
  namespace,
  handleMoveCommentAndSave,
  handleScaleUp,
  handleScaleDown,
  handleAddPR,
  lastUpdated,
  onRefresh
}) {
  const [selectedPrId, setSelectedPrId] = useState(null);
  const [isPendingOpen, setIsPendingOpen] = useState(false);
  const [isExcludedOpen, setIsExcludedOpen] = useState(false);
  const [excludedDetails, setExcludedDetails] = useState({});

  useEffect(() => {
    if (activeRepo) {
         const fetchDetails = (id) => {
             if (!excludedDetails[id]) {
                fetch(`/api/repo/${activeRepo.name}/prs/${id}/details`)
                    .then(res => {
                        if (res.ok) return res.json();
                        throw new Error("Failed");
                    })
                    .then(data => {
                        setExcludedDetails(prev => ({ ...prev, [id]: data }));
                    })
                    .catch(() => {});
             }
         };

         if (activeRepo.excludePullRequests) {
            activeRepo.excludePullRequests.forEach(p => fetchDetails(typeof p === 'object' ? p.id : p));
         }
         
         if (activeRepo.pendingPRs) {
            activeRepo.pendingPRs.forEach(p => fetchDetails(typeof p === 'object' ? p.number : p));
         }
    }
  }, [activeRepo]);

  // Categorize PRs (Active, Pending, Excluded)
  // 1. Active PRs
  const activeList = [];
  if (prs.length > 0) {
      prs.forEach(pr => activeList.push({ ...pr, id: String(pr.id), type: 'active', sortId: parseInt(pr.id) }));
  }

  const getPriority = (pr) => {
      const isReviewDraftCreated = pr.reviewState === 'submitted' || !!pr.review;
      if (isReviewDraftCreated) return 5;

      if (!pr.agentState) return 6;

      const state = pr.agentState.toLowerCase();
      if (state === 'review ready') return 1;
      if (state.includes('error')) return 2;
      if (state === 'quota exceeded') return 3;
      if (state === 'too many files') return 4;

      return 6;
  };

  activeList.sort((a, b) => {
      const pA = getPriority(a);
      const pB = getPriority(b);
      if (pA !== pB) return pA - pB;
      return b.sortId - a.sortId;
  });

  // 2. Pending PRs
  const pending = activeRepo?.pendingPRs || [];
  let pendingList = [];
  pending.forEach(p => {
       // Handle both old format (number) and new format (object)
       const id = typeof p === 'object' ? p.number : p;

       const details = excludedDetails[id];
       let title = details && details.title ? `PR #${id}: ${details.title}` : `PR #${id}`;
       let htmlURL = details ? details.htmlURL : null;
       
       if (!details && typeof p === 'object' && p.title) {
            title = `PR #${id}: ${p.title}`;
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

  // 3. Excluded PRs
  const excluded = activeRepo?.excludePullRequests || [];
  const excludedList = [];
  excluded.forEach(p => {
       if (!activeList.find(i => i.sortId === parseInt(p))) {
           const details = excludedDetails[p];
           const title = details && details.title ? `PR #${p}: ${details.title}` : `PR #${p} (Excluded)`;
           excludedList.push({ id: p.toString(), type: 'excluded', sortId: p, title: title });
       }
  });
  excludedList.sort((a, b) => b.sortId - a.sortId);

  // Auto-select first active PR if none selected
  useEffect(() => {
    if (!selectedPrId && activeList.length > 0) {
        setSelectedPrId(activeList[0].id);
    }
  }, [activeList, selectedPrId]);

  const allItems = [...activeList, ...pendingList, ...excludedList];
  const selectedPr = allItems.find(p => p.id === selectedPrId);

  const getReviewFlairColor = (flairText) => {
    if (!flairText) return '#3e7f67ff';
    const text = flairText.toLowerCase();
    if (text === 'done' || text === 'review ready') return 'green';
    if (text.includes('reviewing')) return 'orange';
    if (text.includes('error')) return '#9e2a2aff';
    if (text === 'submitted' || text === 'review draft created') return '#3f5398ff';
    return '#cd9945ff'; // Default color
  };

  const renderSidebarItem = (item) => {
      let flairText = '';
      if (item.type === 'active') {
          if (item.reviewState === 'submitted') {
              flairText = 'Review Draft Created';
          } else if (item.agentState) {
              flairText = item.agentState;
          } else if (drafts[item.id] && drafts[item.id].note && drafts[item.id].note.trim() !== '') {
              flairText = 'Ready';
          } else {
              flairText = 'Generating...';
          }
      }

      return (
          <div 
              key={item.id} 
              className={`sidebar-item ${selectedPrId === item.id ? 'selected' : ''} ${item.type}`}
              onClick={() => setSelectedPrId(item.id)}
          >
              <div className="sidebar-item-header">
                  <span className="sidebar-id">#{item.id}</span>
                  {flairText && item.type === 'active' && (
                      <span className="sidebar-flair" style={{ backgroundColor: getReviewFlairColor(flairText) }}>
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

  return (
    <div className="review-container">
        <div className="review-sidebar">
            <div className="sidebar-section">
                <h5 className="sidebar-header">
                    Active ({activeList.length}/{activeRepo?.review?.maxActiveSandboxes ?? '?'})
                </h5>
                {activeList.map(renderSidebarItem)}
                <div className="sidebar-item add-pr" onClick={() => handleAddPR()} style={{textAlign: 'center', cursor: 'pointer', color: 'var(--text-secondary)', border: '1px dashed var(--border-color)'}}>
                    + Add PR
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
            {selectedPr ? (
                <PrReviewCard
                    key={selectedPr.id}
                    pr={selectedPr}
                    drafts={drafts}
                    collapsedReviews={{ [selectedPr.id]: false }} // Always expanded in main view
                    reviewViewModes={reviewViewModes}
                    yamlDrafts={yamlDrafts}
                    handleDelete={handleDelete}
                    handleSaveDraft={handleSaveDraft}
                    handleDraftChange={handleDraftChange}
                    handleRemoveComment={handleRemoveComment}
                    toggleReviewView={toggleReviewView}
                    handleYamlDraftChange={handleYamlDraftChange}
                    handleYamlDraftBlur={handleYamlDraftBlur}
                    handleSubmit={handleSubmit}
                    handleExportCurl={handleExportCurl}
                    getSandboxStatusClass={getSandboxStatusClass}
                    toggleCollapse={() => {}} // No-op, always expanded
                    namespace={namespace}
                    handleMoveCommentAndSave={handleMoveCommentAndSave}
                    handleScaleUp={handleScaleUp}
                    handleScaleDown={handleScaleDown}
                    handleAddPR={handleAddPR}
                    isMainView={true} // Hint to component that it is in main view
                    lastUpdated={lastUpdated}
                    repoName={activeRepo.name}
                    onRefresh={onRefresh}
                    availableModels={activeRepo.review?.models}
                />
            ) : (
                <div className="empty-state">
                    <p>Select a PR to view details or add a new one.</p>
                </div>
            )}
        </div>
    </div>
  );
}

export default Review;
