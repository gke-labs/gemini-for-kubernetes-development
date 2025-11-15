import React, { useState, useEffect, useCallback } from 'react';
import yaml from 'js-yaml';
import './App.css';
import PrReviewCard from './PrReviewCard';
import IssueCard from './IssueCard';
import AddRepo from './AddRepo';
import DeleteRepo from './DeleteRepo';
import Settings from './Settings';

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isGuest, setIsGuest] = useState(false);
  const [isLoadingAuth, setIsLoadingAuth] = useState(true);
  const [user, setUser] = useState(null);
  const [view, setView] = useState('dashboard'); // 'dashboard', 'settings', 'add_repo'
  const [githubAuthEnabled, setGithubAuthEnabled] = useState(false);
  const [showGithubConfig, setShowGithubConfig] = useState(false);
  const [githubClientId, setGithubClientId] = useState('');
  const [githubClientSecret, setGithubClientSecret] = useState('');
  const [configError, setConfigError] = useState('');

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

  // Check authentication status on load
  useEffect(() => {
    fetch('/api/auth/status')
      .then(res => {
        if (res.ok) return res.json();
        throw new Error("Not authenticated");
      })
      .then(data => {
        setIsAuthenticated(true);
        setUser(data.user);
        setIsLoadingAuth(false);
      })
      .catch(() => {
        setIsAuthenticated(false);
        setIsLoadingAuth(false);
      });
      
    fetch('/api/auth/providers')
      .then(res => res.json())
      .then(data => {
        setGithubAuthEnabled(data.github);
      })
      .catch(err => console.error("Failed to fetch auth providers:", err));
  }, []);

  const handleGithubConfigSubmit = (e) => {
    e.preventDefault();
    setConfigError('');
    fetch('/api/auth/github-config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ client_id: githubClientId, client_secret: githubClientSecret })
    })
    .then(async (res) => {
      if (res.ok) {
        setGithubAuthEnabled(true);
        setShowGithubConfig(false);
      } else {
        const data = await res.json();
        throw new Error(data.error || 'Failed to update config');
      }
    })
    .catch(err => setConfigError(err.message));
  };

  const fetchRepos = useCallback(() => {
    if (!isAuthenticated && !isGuest) return;
    fetch('/api/repos')
      .then(res => res.json())
      .then(data => {
        const safeData = data || [];
        setRepos(safeData);
        // If we have an active repo, make sure it still exists
        if (activeRepo && !safeData.find(r => r.name === activeRepo.name)) {
           setActiveRepo(null);
        }
        // If no active repo and we have repos, select the first one
        if (!activeRepo && safeData.length > 0 && view === 'dashboard') {
           handleRepoClick(safeData[0].name, safeData);
        }
      })
      .catch(err => console.error("Failed to fetch repos:", err));
  }, [isAuthenticated, isGuest, activeRepo, view]);

  useEffect(() => {
    if (isAuthenticated || isGuest) {
        fetchRepos();
    }
  }, [isAuthenticated, isGuest, fetchRepos]);

  useEffect(() => {
    if ((!isAuthenticated && !isGuest) || view !== 'dashboard') return;

    if (activeRepo && activeSubTab.repo === activeRepo.name) {
      if (activeSubTab.name === 'review') {
        setIssues([]);
        fetch(`/api/repo/${activeRepo.name}/prs`)
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
        fetch(`/api/repo/${activeRepo.name}/issues/${activeSubTab.name}`)
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
  }, [activeRepo, activeSubTab, isAuthenticated, isGuest, view]);

  const handleLogin = () => {
    window.location.href = '/api/auth/login';
  };

  const handleGuestLogin = () => {
    setIsGuest(true);
  };

  const handleLogout = () => {
    if (isGuest) {
        setIsGuest(false);
        setRepos([]);
        setActiveRepo(null);
        return;
    }
    fetch('/api/auth/logout', { method: 'POST' })
      .then(() => {
        setIsAuthenticated(false);
        setUser(null);
        setRepos([]);
        setActiveRepo(null);
      })
      .catch(err => console.error("Failed to logout", err));
  };

  const handleRepoClick = (repoName, currentRepos = repos) => {
    setView('dashboard');
    const repo = currentRepos.find(r => r.name === repoName);
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

  const handleRepoDeleted = (deletedRepoName) => {
    fetchRepos();
    if (activeRepo && activeRepo.name === deletedRepoName) {
      setActiveRepo(null);
    }
  };

  const handleDelete = (id) => {
    fetch(`/api/repo/${activeRepo.name}/prs/${id}`, { method: 'DELETE' })
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
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/draft`, {
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
      fetch(`/api/repo/${activeRepo.name}/prs/${id}/draft`, {
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
    fetch(`/api/repo/${activeRepo.name}/prs/${id}/submitreview`, {
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

  const handleExportCurl = (id, onSuccess) => {
    let review;
    if (reviewViewModes[id] === 'yaml') {
      try {
        review = yaml.load(yamlDrafts[id]);
      } catch (e) {
        alert('Invalid YAML. Please fix it before exporting.');
        return;
      }
    } else {
      review = drafts[id];
    }

    if (!review || (!review.review.body?.trim() && (!review.review.comments || review.review.comments.length === 0))) {
      alert("Please leave a review comment before Exporting.");
      return;
    }

    try {
      const url = new URL(activeRepo.url);
      const pathParts = url.pathname.split('/').filter(p => p);
      if (pathParts.length < 2) {
        alert("Invalid repo URL format");
        return;
      }
      const owner = pathParts[0];
      const repoName = pathParts[1];

      const reviewRequest = review.review;
      // Ensure event is not set (draft)
      const requestBody = { ...reviewRequest };
      delete requestBody.event;

      // Filter out null values from comments
      if (requestBody.comments) {
        requestBody.comments = requestBody.comments.map(comment => {
          const cleanComment = {};
          Object.keys(comment).forEach(key => {
            if (comment[key] !== null && comment[key] !== undefined) {
              cleanComment[key] = comment[key];
            }
          });
          return cleanComment;
        });
      }

      const jsonBody = JSON.stringify(requestBody);
      // Escape single quotes for bash single-quoted string using unicode escape
      const escapedJSONBody = jsonBody.replace(/'/g, '\\u0027');

      const curlCmd = `curl -L \\
  -X POST \\
  -H "Accept: application/vnd.github+json" \\
  -H "Authorization: Bearer <YOUR_TOKEN>" \\
  -H "X-GitHub-Api-Version: 2022-11-28" \\
  https://api.github.com/repos/${owner}/${repoName}/pulls/${id}/reviews \\
  -d '${escapedJSONBody}'`;

      if (onSuccess) {
        onSuccess(curlCmd);
      }
    } catch (e) {
      console.error("Failed to generate curl command:", e);
      alert("Failed to generate curl command: " + e.message);
    }
  };

  const handleIssueSaveDraft = (issueId, handlerName) => {
    const draft = drafts[issueId];
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/handler/${handlerName}/draft`, {
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
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/handler/${handlerName}/submitcomment`, {
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
    fetch(`/api/repo/${activeRepo.name}/issues/${issueId}/handler/${handlerName}`, { method: 'DELETE' })
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
    if (!activeRepo) return <p>Please select or add a repository to watch.</p>;
    if (activeSubTab.name === 'review') {
      if (prs.length === 0) return <p>No active Pull Requests found for this repository.</p>;
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
          handleExportCurl={handleExportCurl}
          getSandboxStatusClass={getSandboxStatusClass}
          toggleCollapse={toggleCollapse}
        />
      ));
    } else {
      if (issues.length === 0) return <p>No active Issues found for this handler.</p>;
      return issues.map(issue => (
        <IssueCard
          key={issue.id}
          issue={issue}
          drafts={drafts}
          activeSubTab={activeSubTab}
          handleIssueDraftChange={handleIssueDraftChange}
          handleIssueSaveDraft={handleIssueSaveDraft}
          handleIssueSubmit={handleIssueSubmit}
          handleIssueDelete={handleIssueDelete}
          getSandboxStatusClass={getSandboxStatusClass}
        />
      ));
    }
  };

  const renderDashboard = () => (
    <>
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
        <button className="tab-btn add-repo-btn" onClick={() => setView('add_repo')} title="Watch new repository">+</button>
      </nav>
      {activeRepo && (
        <div className="active-repo-container">
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
            <DeleteRepo repo={activeRepo} onRepoDeleted={handleRepoDeleted} />
        </div>
      )}
      <main className="pr-list">
        {renderContent()}
      </main>
    </>
  );

  if (isLoadingAuth) return <div className="App"><header className="App-header"><h1>Loading...</h1></header></div>;

  if (!isAuthenticated && !isGuest) {
    return (
      <div className="App">
        <header className="App-header">
          <h1>Repo Agent</h1>
          <div className="theme-switch-wrapper">
            <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
          </div>
        </header>
        <main className="login-container">
          <h2>Welcome to Repo Agent</h2>
          <p>Please log in with GitHub to manage your review sandboxes.</p>
          <div className="login-actions">
            {githubAuthEnabled ? (
                <button className="btn btn-submit" onClick={handleLogin}>Login with GitHub</button>
            ) : (
                <div className="github-config-section">
                    <p className="message info">GitHub OAuth is not configured. You can continue as Guest or configure it below.</p>
                    {!showGithubConfig ? (
                        <button className="btn" onClick={() => setShowGithubConfig(true)}>Configure GitHub OAuth</button>
                    ) : (
                        <form onSubmit={handleGithubConfigSubmit} className="settings-form">
                            {configError && <div className="message error">{configError}</div>}
                            <div className="form-group">
                                <label>Client ID:</label>
                                <input type="text" value={githubClientId} onChange={e => setGithubClientId(e.target.value)} required />
                            </div>
                            <div className="form-group">
                                <label>Client Secret:</label>
                                <input type="password" value={githubClientSecret} onChange={e => setGithubClientSecret(e.target.value)} required />
                            </div>
                            <div className="form-actions">
                                <button type="submit" className="btn btn-submit">Save & Enable</button>
                                <button type="button" className="btn" onClick={() => setShowGithubConfig(false)}>Cancel</button>
                            </div>
                        </form>
                    )}
                </div>
            )}
            <button className="btn" onClick={handleGuestLogin}>Continue as Guest</button>
          </div>
        </main>
      </div>
    );
  }

  return (
    <div className="App">
      <header className="App-header">
        <h1><a href="/" onClick={(e) => { e.preventDefault(); setView('dashboard'); }}>Repo Agent</a></h1>
        <div className="header-right">
          {user && <span className="user-greeting">Hi, {user}</span>}
          {isGuest && <span className="user-greeting">Guest</span>}
          <button className="btn" onClick={() => setView('settings')} style={{marginRight: '10px'}}>Settings</button>
          <button className="btn btn-delete" onClick={handleLogout} style={{marginRight: '20px'}}>Logout</button>
          <div className="theme-switch-wrapper">
            <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
          </div>
        </div>
      </header>
      
      {view === 'dashboard' && renderDashboard()}
      {view === 'settings' && <Settings onBack={() => setView('dashboard')} />}
      {view === 'add_repo' && <AddRepo onCancel={() => setView('dashboard')} onRepoAdded={() => { fetchRepos(); setView('dashboard'); }} />}

    </div>
  );
}

export default App;
