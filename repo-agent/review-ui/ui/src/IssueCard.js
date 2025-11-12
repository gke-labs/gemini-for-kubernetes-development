import React, { useState } from 'react';

function IssueCard({
  issue,
  drafts,
  activeSubTab,
  handleIssueDraftChange,
  handleIssueSaveDraft,
  handleIssueSubmit,
  handleIssueDelete,
  getSandboxStatusClass,
}) {
  const [isCollapsed, setIsCollapsed] = useState(true);

  const toggleCollapse = () => {
    setIsCollapsed(!isCollapsed);
  };

  return (
    <div key={issue.id} className={`pr-card ${issue.comment ? 'review-submitted' : ''}`}>
      <div className="pr-card-header" onClick={toggleCollapse}>
        <h3>
          <a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title} (Issue #{issue.id})</a>
          <span style={{ marginLeft: '10px', fontSize: 'small', color: '#555' }}>
            {isCollapsed ? 'click to expand' : 'click to collapse'}
          </span>
        </h3>
        {getSandboxStatusClass(issue) === 'green' ? (
          <a href={`/sandbox/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
            Sandbox: {issue.sandbox}
          </a>
        ) : (
          <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox: {issue.sandbox || 'Not created'}</span>
        )}
      </div>
      {!isCollapsed && (
        <>
          {issue.pushBranch ? (
            <div className="branch-link">
              <strong>Branch: </strong> <a href={issue.branchURL} target="_blank" rel="noopener noreferrer">{issue.branchURL}</a>
            </div>
          ) : issue.comment ? (
            <div className="review-display">
              <strong>Comment:</strong>
              <p>{issue.comment}</p>
            </div>
          ) : (
            <textarea
              className="review-textarea"
              value={drafts[issue.id] || ''}
              onChange={(e) => handleIssueDraftChange(issue.id, e.target.value)}
              onBlur={() => handleIssueSaveDraft(issue.id, activeSubTab.name)}
              placeholder="Leave a comment..."
            ></textarea>
          )}
          <div className="pr-card-actions">
            {!issue.pushBranch && (
              <button className="btn btn-submit" onClick={() => handleIssueSubmit(issue.id, activeSubTab.name)} disabled={!!issue.comment}>
                {issue.comment ? 'Submitted' : 'Submit'}
              </button>
            )}
            <button className="btn btn-delete" onClick={() => handleIssueDelete(issue.id, activeSubTab.name)}>Delete</button>
          </div>
        </>
      )}
    </div>
  );
}

export default IssueCard;
