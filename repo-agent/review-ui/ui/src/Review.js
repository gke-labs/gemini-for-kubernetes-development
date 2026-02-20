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
  onRefresh,
  showToast,
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
  const pending = activeRepo.pendingPRs || [];
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
  const excluded = activeRepo.excludePullRequests || [];
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
    if (!flairText) return '#3b82f6';
    const text = flairText.toLowerCase();
    if (text === 'done' || text === 'review ready') return '#22c55e';
    if (text.includes('reviewing') || text.includes('generating')) return '#f59e0b';
    if (text.includes('error')) return '#ef4444';
    if (text === 'submitted' || text === 'review draft created') return '#3b82f6';
    return '#f59e0b';
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
                      <span className={`sidebar-flair ${flairText.toLowerCase().includes('generating') || flairText.toLowerCase().includes('reviewing') ? 'status-badge-running' : ''}`} style={{ backgroundColor: getReviewFlairColor(flairText) + '1a', color: getReviewFlairColor(flairText), padding: '2px 6px', borderRadius: '4px', fontSize: '10px', fontWeight: '700', textTransform: 'uppercase' }}>
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
            {/* Section header with count */}
            <div className="sidebar-section-header">
                <span className="sidebar-section-title">Active Reviews</span>
                <span className="sidebar-section-count">{activeList.length}</span>
            </div>
            <div className="sidebar-section">
                {!lastUpdated && activeList.length === 0 && prs.length === 0 && (
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
                <div className="sidebar-item add-pr" onClick={() => handleAddPR()} style={{textAlign: 'center', cursor: 'pointer', color: 'var(--color-primary)', border: '1px dashed var(--color-primary)', borderRadius: '8px', margin: '8px 12px', opacity: 0.7}}>
                    <span className="material-symbols-outlined" style={{fontSize: '16px', marginRight: '4px'}}>add</span> Add PR
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
            {!lastUpdated && !selectedPr && prs.length === 0 && activeList.length === 0 ? (
                <div style={{padding: '24px'}}>
                    {/* Skeleton: PR header card */}
                    <div style={{border: '1px solid var(--border-color)', borderRadius: '12px', padding: '20px', marginBottom: '16px'}}>
                        <div style={{display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '16px'}}>
                            <div className="skeleton" style={{width: '60px', height: '16px', borderRadius: '4px'}}></div>
                            <div className="skeleton" style={{width: '200px', height: '16px', borderRadius: '4px'}}></div>
                            <div style={{marginLeft: 'auto'}} className="skeleton" ><div style={{width: '80px', height: '24px', borderRadius: '4px'}}></div></div>
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
            ) : selectedPr ? (
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
                    showToast={showToast}
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
