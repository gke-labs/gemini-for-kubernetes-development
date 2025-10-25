import React, { useState, useEffect } from 'react';
import './App.css';

function App() {
  const [repos, setRepos] = useState([]);
  const [activeRepo, setActiveRepo] = useState('');
  const [activeSubTab, setActiveSubTab] = useState({ repo: '', name: '' });
  const [prs, setPrs] = useState([]);
  const [issues, setIssues] = useState([]);
  const [drafts, setDrafts] = useState({});

  useEffect(() => {
    fetch('/api/repos')
      .then(res => res.json())
      .then(data => {
        setRepos(data);
        if (data.length > 0) {
          const firstRepo = data[0];
          setActiveRepo(firstRepo.name);
          if (firstRepo.review) {
            setActiveSubTab({ repo: firstRepo.name, name: 'review' });
          } else if (firstRepo.issueHandlers && firstRepo.issueHandlers.length > 0) {
            setActiveSubTab({ repo: firstRepo.name, name: firstRepo.issueHandlers[0].name });
          }
        }
      })
      .catch(err => console.error("Failed to fetch repos:", err));
  }, []);

  useEffect(() => {
    if (activeRepo && activeSubTab.repo === activeRepo) {
      if (activeSubTab.name === 'review') {
        fetch(`/api/repo/${activeRepo}/prs`)
          .then(res => res.json())
          .then(data => {
            setPrs(data);
            const initialDrafts = {};
            data.forEach(pr => {
              initialDrafts[pr.id] = pr.draft || '';
            });
            setDrafts(initialDrafts);
          })
          .catch(err => console.error(`Failed to fetch PRs for ${activeRepo}:`, err));
      } else if (activeSubTab.name) {
        fetch(`/api/repo/${activeRepo}/issues/${activeSubTab.name}`)
          .then(res => res.json())
          .then(data => {
            setIssues(data);
            const initialDrafts = {};
            data.forEach(issue => {
              initialDrafts[issue.id] = issue.draft || '';
            });
            setDrafts(initialDrafts);
          })
          .catch(err => console.error(`Failed to fetch issues for ${activeRepo} handler ${activeSubTab.name}:`, err));
      }
    }
  }, [activeRepo, activeSubTab]);

  const handleRepoClick = (repoName) => {
    setActiveRepo(repoName);
    const repo = repos.find(r => r.name === repoName);
    if (repo) {
      if (repo.review) {
        setActiveSubTab({ repo: repoName, name: 'review' });
      } else if (repo.issueHandlers && repo.issueHandlers.length > 0) {
        setActiveSubTab({ repo: repoName, name: repo.issueHandlers[0].name });
      } else {
        setActiveSubTab({ repo: repoName, name: '' });
      }
    }
  };

  const handleDelete = (id) => {
    fetch(`/api/repo/${activeRepo}/prs/${id}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
          setPrs(prs.filter(pr => pr.id !== id));
        } else {
          alert("Failed to delete PR sandbox");
        }
      })
      .catch(err => console.error("Failed to delete PR:", err));
  };

  const handleSaveDraft = (id) => {
    const draft = drafts[id];
    fetch(`/api/repo/${activeRepo}/prs/${id}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save draft:", err));
  };

  const handleDraftChange = (id, value) => {
    setDrafts(prevDrafts => ({
      ...prevDrafts,
      [id]: value
    }));
  };

  const handleSubmit = (id) => {
    const review = drafts[id];
    if (!review.trim()) {
      alert("Please leave a review comment before Submitting.");
      return;
    }
    fetch(`/api/repo/${activeRepo}/prs/${id}/submitreview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ review })
    })
    .then(res => {
      if (res.ok) {
        setPrs(prs.map(pr => pr.id === id ? { ...pr, review, draft: '' } : pr));
      } else {
        alert("Failed to submit PR review");
      }
    })
    .catch(err => console.error("Failed to submit PR review:", err));
  };

  const handleIssueSaveDraft = (issueId, handlerName) => {
    const draft = drafts[issueId];
    fetch(`/api/repo/${activeRepo}/issues/${issueId}/handler/${handlerName}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save issue draft:", err));
  };

  const handleIssueSubmit = (issueId, handlerName) => {
    const comment = drafts[issueId];
    if (!comment.trim()) {
      alert("Please leave a comment before Submitting.");
      return;
    }
    fetch(`/api/repo/${activeRepo}/issues/${issueId}/handler/${handlerName}/submitcomment`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ comment })
    })
    .then(res => {
      if (res.ok) {
        setIssues(issues.map(issue => issue.id === issueId ? { ...issue, comment, draft: '' } : issue));
      } else {
        alert("Failed to submit issue comment");
      }
    })
    .catch(err => console.error("Failed to submit issue comment:", err));
  };

  const handleIssueDelete = (issueId, handlerName) => {
    fetch(`/api/repo/${activeRepo}/issues/${issueId}/handler/${handlerName}`, { method: 'DELETE' })
      .then(res => {
        if (res.ok) {
          setIssues(issues.filter(issue => issue.id !== issueId));
        } else {
          alert("Failed to delete issue sandbox");
        }
      })
      .catch(err => console.error("Failed to delete issue:", err));
  };

  const getSandboxStatusClass = (item) => {
    if (!item.sandbox) {
      return 'grey';
    }
    if (item.sandboxReplica === "0") {
      return 'yellow';
    }
    return 'green';
  };

  const renderContent = () => {
    if (activeSubTab.name === 'review') {
      return prs.map(pr => (
        <div key={pr.id} className={`pr-card ${pr.review ? 'review-submitted' : ''}`}>
          <div className="pr-card-header">
            <h3><a href={pr.htmlURL} target="_blank" rel="noopener noreferrer">{pr.title} (PR #{pr.id})</a></h3>
            {getSandboxStatusClass(pr) === 'green' ? (
              <a href={`/sandbox/${pr.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>
                Sandbox: {pr.sandbox}
              </a>
            ) : (
              <span className={`pr-sandbox ${getSandboxStatusClass(pr)}`}>Sandbox: {pr.sandbox || 'Not created'}</span>
            )}
          </div>
          {pr.review ? (
            <div className="review-display">
              <strong>Review:</strong>
              <p>{pr.review}</p>
            </div>
          ) : (
            <textarea
              className="review-textarea"
              value={drafts[pr.id] || ''}
              onChange={(e) => handleDraftChange(pr.id, e.target.value)}
              onBlur={() => handleSaveDraft(pr.id)}
              placeholder="Leave a review comment..."
            ></textarea>
          )}
          <div className="pr-card-actions">
            <button className="btn btn-submit" onClick={() => handleSubmit(pr.id)} disabled={!!pr.review}>
              {pr.review ? 'Submitted' : 'Submit'}
            </button>
            <button className="btn btn-delete" onClick={() => handleDelete(pr.id)}>Delete</button>
          </div>
        </div>
      ));
    } else {
      return issues.map(issue => (
        <div key={issue.id} className={`pr-card ${issue.comment ? 'review-submitted' : ''}`}>
          <div className="pr-card-header">
            <h3><a href={issue.htmlURL} target="_blank" rel="noopener noreferrer">{issue.title} (Issue #{issue.id})</a></h3>
            {getSandboxStatusClass(issue) === 'green' ? (
              <a href={`/sandbox/${issue.sandbox}/`} target="_blank" rel="noopener noreferrer" className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>
                Sandbox: {issue.sandbox}
              </a>
            ) : (
              <span className={`pr-sandbox ${getSandboxStatusClass(issue)}`}>Sandbox: {issue.sandbox || 'Not created'}</span>
            )}
          </div>
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
              onChange={(e) => handleDraftChange(issue.id, e.target.value)}
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
        </div>
      ));
    }
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>Pull Request and Issue Reviews</h1>
      </header>
      <nav className="repo-tabs">
        {repos.map(repo => (
          <button
            key={repo.name}
            className={`tab-btn ${activeRepo === repo.name ? 'active' : ''}`}
            onClick={() => handleRepoClick(repo.name)}
          >
            {repo.name}
          </button>
        ))}
      </nav>
      {activeRepo && (
        <nav className="sub-tabs">
          {repos.find(r => r.name === activeRepo)?.review && (
            <button
              className={`sub-tab-btn ${activeSubTab.name === 'review' ? 'active' : ''}`}
              onClick={() => setActiveSubTab({ repo: activeRepo, name: 'review' })}
            >
              Review
            </button>
          )}
          {repos.find(r => r.name === activeRepo)?.issueHandlers?.map(handler => (
            <button
              key={handler.name}
              className={`sub-tab-btn ${activeSubTab.name === handler.name ? 'active' : ''}`}
              onClick={() => setActiveSubTab({ repo: activeRepo, name: handler.name })}
            >
              {handler.name}
            </button>
          ))}
        </nav>
      )}
      <main className="pr-list">
        {renderContent()}
      </main>
    </div>
  );
}

export default App;