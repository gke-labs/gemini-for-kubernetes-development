import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';
import './App.css';
import PrReviewCard from './PrReviewCard';

function App() {
  const [repos, setRepos] = useState([]);
  const [activeRepo, setActiveRepo] = useState(null);
  const [activeSubTab, setActiveSubTab] = useState({ repo: '', name: '' });
  const [prs, setPrs] = useState([]);
  const [issues, setIssues] = useState([]);
  const [drafts, setDrafts] = useState({});
  const [collapsedReviews, setCollapsedReviews] = useState({});
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'light');
  const [reviewViewModes, setReviewViewModes] = useState({});
  const [yamlDrafts, setYamlDrafts] = useState({});

  useEffect(() => {
    document.body.className = theme === 'dark' ? 'dark-mode' : '';
    localStorage.setItem('theme', theme);
  }, [theme]);

  useEffect(() => {
    fetch('/api/repos')
      .then(res => res.json())
      .then(data => {
        const safeData = data || [];
        setRepos(safeData);
        if (safeData.length > 0) {
          const firstRepo = safeData[0];
          setActiveRepo(firstRepo);
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
    if (activeRepo && activeSubTab.repo === activeRepo.name) {
      if (activeSubTab.name === 'review') {
        setIssues([]);
        fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/prs`)
          .then(res => res.json())
          .then(data => {
            const safeData = data || [];
            setPrs(safeData);
            const initialDrafts = {};
            const initialCollapsedState = {};
            safeData.forEach(pr => {
              try {
                const parsedDraft = yaml.load(pr.draft || '');
                initialDrafts[pr.id] = parsedDraft || { note: '', review: { body: '', comments: [] } };
              } catch (e) {
                console.error(`Error parsing draft YAML for PR ${pr.id}:`, e);
                initialDrafts[pr.id] = { note: '', review: { body: '', comments: [] } };
              }
              initialCollapsedState[pr.id] = true; // Collapse by default
            });
            setDrafts(initialDrafts);
            setCollapsedReviews(initialCollapsedState);
            const initialViewModes = {};
            safeData.forEach(pr => {
              initialViewModes[pr.id] = 'structured';
            });
            setReviewViewModes(initialViewModes);
          })
          .catch(err => console.error(`Failed to fetch PRs for ${activeRepo.name}:`, err));
      } else if (activeSubTab.name) {
        setPrs([]);
        fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/issues/${activeSubTab.name}`)
          .then(res => res.json())
          .then(data => {
            const safeData = data || [];
            setIssues(safeData);
            const initialDrafts = {};
            safeData.forEach(issue => {
              initialDrafts[issue.id] = issue.draft || '';
            });
            setDrafts(initialDrafts);
          })
          .catch(err => console.error(`Failed to fetch issues for ${activeRepo.name} handler ${activeSubTab.name}:`, err));
      }
    }
  }, [activeRepo, activeSubTab]);

  const handleRepoClick = (repoName) => {
    const repo = repos.find(r => r.name === repoName);
    setActiveRepo(repo);
    setPrs([]);
    setIssues([]);
    if (repo) {
      if (repo.review) {
        setActiveSubTab({ repo: repoName, name: 'review' });
      } else if (repo.issueHandlers && repo.issueHandlers.length > 0) {
        setActiveSubTab({ repo: repoName, name: repo.issueHandlers[0].name });
      }
    }
  };

  const handleDelete = (id) => {
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/prs/${id}`, { method: 'DELETE' })
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
    const draft = yaml.dump(drafts[id]);
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/prs/${id}/draft`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ draft })
    }).catch(err => console.error("Failed to save draft:", err));
  };

  const handleDraftChange = (id, field, value, index = null) => {
    setDrafts(prevDrafts => {
      const newDraft = { ...prevDrafts[id] };
      if (field === 'note') {
        newDraft.note = value;
      } else if (field === 'review.body') {
        newDraft.review = { ...newDraft.review, body: value };
      } else if (field === 'comment.body' && index !== null) {
        newDraft.review.comments[index] = { ...newDraft.review.comments[index], body: value };
      }
      return { ...prevDrafts, [id]: newDraft };
    });
  };

  const handleRemoveComment = (id, index) => {
    setDrafts(prevDrafts => {
      const newDraft = { ...prevDrafts[id] };
      newDraft.review.comments.splice(index, 1);
      return { ...prevDrafts, [id]: newDraft };
    });
  };

  const handleIssueDraftChange = (issueId, value) => {
    setDrafts(prevDrafts => ({
      ...prevDrafts,
      [issueId]: value
    }));
  };

  const toggleReviewView = (id) => {
    const currentMode = reviewViewModes[id] || 'structured';
    if (currentMode === 'yaml') {
      try {
        const parsedDraft = yaml.load(yamlDrafts[id]);
        setDrafts(prev => ({ ...prev, [id]: parsedDraft }));
        setReviewViewModes(prev => ({ ...prev, [id]: 'structured' }));
      } catch (e) {
        alert('Invalid YAML. Please fix it before switching view.');
        console.error("YAML parse error on view switch:", e);
      }
    } else {
      setYamlDrafts(prev => ({ ...prev, [id]: yaml.dump(drafts[id] || { note: '', review: { body: '', comments: [] } }) }));
      setReviewViewModes(prev => ({ ...prev, [id]: 'yaml' }));
    }
  };

  const handleYamlDraftChange = (id, value) => {
    setYamlDrafts(prev => ({ ...prev, [id]: value }));
  };

  const handleYamlDraftBlur = (id) => {
    try {
      const parsedDraft = yaml.load(yamlDrafts[id]);
      setDrafts(prev => ({ ...prev, [id]: parsedDraft }));
      const draft = yaml.dump(parsedDraft);
      fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/prs/${id}/draft`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ draft })
      }).catch(err => console.error("Failed to save draft:", err));
    } catch (e) {
      alert('Invalid YAML, not saving.');
      console.error("YAML parse error on blur:", e);
    }
  };

  const handleSubmit = (id) => {
    let review;
    if (reviewViewModes[id] === 'yaml') {
      try {
        review = yaml.load(yamlDrafts[id]);
      } catch (e) {
        alert('Invalid YAML. Please fix it before submitting.');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      alert("Please leave a review comment before Submitting.");
      return;
    }
    const reviewYAML = yaml.dump(review);
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/prs/${id}/submitreview`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ review: reviewYAML })
    })
    .then(res => {
      if (res.ok) {
        setPrs(prs.map(pr => pr.id === id ? { ...pr, review: reviewYAML, draft: '' } : pr));
      } else {
        alert("Failed to submit PR review");
      }
    })
    .catch(err => console.error("Failed to submit PR review:", err));
  };

  const handleIssueSaveDraft = (issueId, handlerName) => {
    const draft = drafts[issueId];
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/issues/${issueId}/handler/${handlerName}/draft`, {
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
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/issues/${issueId}/handler/${handlerName}/submitcomment`, {
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
    fetch(`/api/repo/${activeRepo.namespace}/${activeRepo.name}/issues/${issueId}/handler/${handlerName}`, { method: 'DELETE' })
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

  const toggleTheme = () => {
    setTheme(theme === 'light' ? 'dark' : 'light');
  };

  const toggleCollapse = (id) => {
    setCollapsedReviews(prev => ({
      ...prev,
      [id]: !prev[id]
    }));
  };

  const renderContent = () => {
    if (activeSubTab.name === 'review') {
      return prs.map(pr => (
        <PrReviewCard
          key={pr.id}
          pr={pr}
          drafts={drafts}
          collapsedReviews={collapsedReviews}
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
          getSandboxStatusClass={getSandboxStatusClass}
          toggleCollapse={toggleCollapse}
        />
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
        </div>
      ));
    }
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>Repo Agent</h1>
        <div className="theme-switch-wrapper">
          <label className="theme-switch" htmlFor="checkbox">
            <input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} />
            <div className="slider round"></div>
          </label>
        </div>
      </header>
      <nav className="repo-tabs">
        {repos.map(repo => (
          <button
            key={repo.name}
            className={`tab-btn ${activeRepo && activeRepo.name === repo.name ? 'active' : ''}`}
            onClick={() => handleRepoClick(repo.name)}
          >
            {repo.name}
          </button>
        ))}
      </nav>
      {activeRepo && (
        <nav className="sub-tabs">
          {repos.find(r => r.name === activeRepo.name)?.review && (
            <button
              className={`sub-tab-btn ${activeSubTab.name === 'review' ? 'active' : ''}`}
              onClick={() => setActiveSubTab({ repo: activeRepo.name, name: 'review' })}
            >
              Review
            </button>
          )}
          {repos.find(r => r.name === activeRepo.name)?.issueHandlers?.map(handler => (
            <button
              key={handler.name}
              className={`sub-tab-btn ${activeSubTab.name === handler.name ? 'active' : ''}`}
              onClick={() => setActiveSubTab({ repo: activeRepo.name, name: handler.name })}
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