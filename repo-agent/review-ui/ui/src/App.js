import React, { useState, useEffect, useRef, useCallback } from 'react';
import './App.css';
import Settings from './Settings';
import Overseer from './Overseer';
import TokenUsage from './TokenUsage';
import Work from './Work';
import SandboxTerminal from './Terminal';

// Repo Agent shell: the Work board is home (docs/design/repoboard.md §8).
// The legacy RepoWatch dashboard (Review/Issues/Dev tabs) retired with
// design Phase 5; Overseer, Usage, and Settings remain as header views.

// Hash route #/terminal/<ns>/<name>: a full-window terminal page for real
// window management (pop out of the sandbox card, multi-monitor, share the
// link). Hash-based so any static hosting serves it; tmux means every
// window attached to the same sandbox shares one live session.
function terminalRoute() {
  const m = window.location.hash.match(/^#\/terminal\/([A-Za-z0-9._-]+)\/([A-Za-z0-9._-]+)$/);
  return m ? { namespace: m[1], name: m[2] } : null;
}

function TerminalPage({ route }) {
  useEffect(() => {
    document.title = `${route.name} — terminal`;
  }, [route]);
  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column', backgroundColor: '#1e1e1e' }}>
      <div style={{ padding: '6px 10px', color: '#8b949e', fontSize: 'small', textAlign: 'left', fontFamily: 'monospace' }}>
        {route.namespace}/{route.name}
      </div>
      <div style={{ flex: 1, minHeight: 0 }}>
        <SandboxTerminal namespace={route.namespace} sandboxName={route.name} fill />
      </div>
    </div>
  );
}

function App() {
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isLoadingAuth, setIsLoadingAuth] = useState(true);
  const [user, setUser] = useState(null);
  const [isAdmin, setIsAdmin] = useState(false);
  const [view, setView] = useState('work'); // 'work', 'overseer', 'usage', 'settings'
  const [termRoute] = useState(terminalRoute());
  const [githubAuthEnabled, setGithubAuthEnabled] = useState(false);
  const [providersError, setProvidersError] = useState(false);
  const [isGeminiKeySet, setIsGeminiKeySet] = useState(true); // Default to true to avoid flash of warning
  const hasRedirectedMissingKey = useRef(false);
  const [theme, setTheme] = useState(localStorage.getItem('theme') || 'light');

  // Feedback modal
  const [feedbackModalOpen, setFeedbackModalOpen] = useState(false);
  const [feedbackTitle, setFeedbackTitle] = useState('');
  const [feedbackText, setFeedbackText] = useState('');
  const [feedbackImage, setFeedbackImage] = useState('');
  const [isSubmittingFeedback, setIsSubmittingFeedback] = useState(false);

  useEffect(() => {
    document.body.className = theme === 'dark' ? 'dark-mode' : '';
    localStorage.setItem('theme', theme);
  }, [theme]);

  // A failed providers fetch means the API is unreachable — different from
  // auth being unconfigured. Retry until we get a real answer.
  const fetchProviders = useCallback(() => {
    fetch('/api/auth/providers')
      .then(res => { if (!res.ok) throw new Error(res.statusText); return res.json(); })
      .then(data => {
        setGithubAuthEnabled(data.github);
        setProvidersError(false);
      })
      .catch(err => {
        console.error("Failed to fetch auth providers:", err);
        setProvidersError(true);
      });
  }, []);

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
        setIsAdmin(data.isAdmin);
        setIsLoadingAuth(false);
      })
      .catch(() => {
        setIsAuthenticated(false);
        setIsLoadingAuth(false);
      });

    fetchProviders();
  }, [fetchProviders]);

  useEffect(() => {
    if (!providersError) return;
    const t = setInterval(fetchProviders, 10000);
    return () => clearInterval(t);
  }, [providersError, fetchProviders]);

  useEffect(() => {
    if (isAuthenticated) {
      fetch('/api/settings')
        .then(res => res.json())
        .then(data => {
          setIsGeminiKeySet(data.gemini_api_key_set);
          if (!data.gemini_api_key_set && !hasRedirectedMissingKey.current && isAuthenticated) {
            hasRedirectedMissingKey.current = true;
            setView('settings');
          }
        })
        .catch(err => console.error("Failed to fetch settings:", err));
    }
  }, [isAuthenticated]);

  const handleLogin = (scope) => {
    window.location.href = `/api/auth/login?scope=${scope}`;
  };

  const handleLogout = () => {
    fetch('/api/auth/logout', { method: 'POST' })
      .then(() => {
        setIsAuthenticated(false);
        setUser(null);
      })
      .catch(err => console.error("Failed to logout", err));
  };

  // Sandbox status coloring shared with the Overseer admin view.
  const getSandboxStatusClass = (item) => {
    if (!item.sandbox) {
      return 'grey';
    }
    if (item.sandboxStatus && (item.sandboxStatus.startsWith('Evicted') || item.sandboxStatus.startsWith('fail:') || item.sandboxStatus === 'Failed')) {
      return 'red';
    }
    if (item.sandboxReplica === "0") {
      return 'yellow';
    }
    return 'green';
  };

  const toggleTheme = () => {
    setTheme(theme === 'light' ? 'dark' : 'light');
  };

  const handleFeedbackClick = async () => {
    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({
        video: { cursor: "always" },
        audio: false
      });
      const video = document.createElement("video");
      video.srcObject = stream;
      video.onloadedmetadata = () => {
        video.play();
        const canvas = document.createElement("canvas");
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
        const ctx = canvas.getContext("2d");
        ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
        const image = canvas.toDataURL("image/png");
        setFeedbackImage(image);
        setFeedbackModalOpen(true);
        stream.getTracks().forEach(track => track.stop());

        // Automatically open image in new tab
        const w = window.open("");
        if (w) {
            w.document.write('<img src="' + image + '" style="max-width: 100%;" />');
            // Attempt to keep focus on current window (background the new tab)
            try {
                w.blur();
                window.focus();
            } catch (e) {
                // ignore
            }
        }
      };
    } catch (err) {
      console.error("Error capturing screen:", err);
      // Fallback to text only if user cancels or error
      setFeedbackImage('');
      setFeedbackModalOpen(true);
    }
  };

  const submitFeedback = () => {
    setIsSubmittingFeedback(true);
    fetch('/api/feedback', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: feedbackTitle, text: feedbackText, image: feedbackImage })
    })
    .then(res => {
        if (res.ok) {
            res.json().then(data => {
                alert(`Feedback submitted successfully!`);
                if (data.issue_url) {
                    window.open(data.issue_url, '_blank');
                }
                setFeedbackModalOpen(false);
                setFeedbackTitle('');
                setFeedbackText('');
                setFeedbackImage('');
            });
        } else {
            res.json().then(data => alert("Failed to submit feedback: " + (data.error || res.statusText)));
        }
    })
    .catch(err => alert("Failed to submit feedback: " + err))
    .finally(() => setIsSubmittingFeedback(false));
  };

  // The standalone terminal window bypasses the shell chrome entirely
  // (auth still applies: the websocket endpoint sits behind the session).
  if (termRoute && isAuthenticated) return <TerminalPage route={termRoute} />;

  if (isLoadingAuth) return <div className="App"><header className="App-header"><h1>Loading...</h1></header></div>;

  if (!isAuthenticated) {
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
          <div className="login-actions">
            {githubAuthEnabled ? (
                <>
                <button className="btn btn-submit" onClick={() => handleLogin('readwrite')} style={{backgroundColor: '#0366d6', marginRight: '10px'}}>Login with GitHub (Read-Write)</button>
                <button className="btn btn-submit" onClick={() => handleLogin('readonly')} style={{backgroundColor: '#6f42c1'}}>Login with GitHub (Read-Only)</button>
                </>
            ) : providersError ? (
                <div className="auth-error">
                    <p>Cannot reach the Repo Agent API right now — retrying…</p>
                    <p style={{fontSize: 'small', color: 'var(--text-secondary)'}}>The backend may be restarting or rescheduling. This page will recover automatically.</p>
                </div>
            ) : (
                <div className="auth-error">
                    <p>GitHub Authentication is not configured.</p>
                    <p>Please set <code>GITHUB_CLIENT_ID</code> and <code>GITHUB_CLIENT_SECRET</code> in the cluster secrets.</p>
                </div>
            )}
          </div>
        </main>
      </div>
    );
  }

  return (
    <div className="App">
      <header className="App-header">
        <h1><a href="/" onClick={(e) => { e.preventDefault(); setView('work'); }}>Repo Agent</a></h1>
        <div className="header-right">
          {user && <span className="user-greeting">Hi, {user}</span>}

          <button className="btn" onClick={() => setView('work')} style={{marginRight: '10px', backgroundColor: '#1a7f37', color: 'white'}}>
              Work
          </button>
          {isAdmin && (
            <button className="btn" onClick={() => setView('overseer')} style={{marginRight: '10px', backgroundColor: '#6f42c1', color: 'white'}}>
                Overseer
            </button>
          )}
          <button className="btn" onClick={() => setView('usage')} style={{marginRight: '10px', backgroundColor: '#0d6efd', color: 'white'}}>
              Usage
          </button>
          <button className="btn" onClick={handleFeedbackClick} style={{marginRight: '10px', backgroundColor: '#28a745'}}>Feedback</button>
          <button className="btn" onClick={() => setView('settings')} style={{marginRight: '10px'}}>Settings</button>
          <button className="btn btn-delete" onClick={handleLogout} style={{marginRight: '20px'}}>Logout</button>
          <div className="theme-switch-wrapper">
            <label className="theme-switch" htmlFor="checkbox"><input type="checkbox" id="checkbox" onChange={toggleTheme} checked={theme === 'dark'} /><div className="slider round"></div></label>
          </div>
        </div>
      </header>

      {isAuthenticated && !isGeminiKeySet && (
        <div className="warning-banner">
          <strong>⚠️ Gemini API Key Missing:</strong> Please configure your Gemini API Key in <a href="#" onClick={(e) => { e.preventDefault(); setView('settings'); }}>Settings</a> to enable fixes, reviews and triage.
        </div>
      )}

      {view === 'work' && <Work namespace={user || 'default'} />}
      {view === 'overseer' && <Overseer onBack={() => setView('work')} getSandboxStatusClass={getSandboxStatusClass} namespace={user || 'default'} />}
      {view === 'usage' && <TokenUsage onBack={() => setView('work')} />}
      {view === 'settings' && <Settings onBack={() => setView('work')} />}

      {feedbackModalOpen && (
        <div className="modal-overlay" onClick={() => setFeedbackModalOpen(false)}>
            <div className="modal-content" onClick={(e) => e.stopPropagation()} style={{maxHeight: '90vh', overflowY: 'auto'}}>
                <h4>Send Feedback</h4>
                {feedbackImage && (
                    <>
                        <div style={{border: '1px solid #ccc', padding: '5px', maxHeight: '300px', overflow: 'hidden'}}>
                            <img
                                src={feedbackImage}
                                alt="Screenshot"
                                style={{maxWidth: '100%', display: 'block', cursor: 'pointer'}}
                                title="Click to open in new tab"
                                onClick={() => {
                                    const w = window.open("");
                                    if (w) {
                                        w.document.write('<img src="' + feedbackImage + '" style="max-width: 100%;" />');
                                    }
                                }}
                            />
                        </div>
                        <p style={{fontSize: '0.9em', color: '#555', marginTop: '5px', marginBottom: '5px'}}>
                           <strong>Note:</strong> The screenshot has been opened in a new tab. You can also click the image above to open it again. Please copy and manually paste the screenshot into the GitHub issue that will be created after you click "Send Feedback".
                        </p>
                    </>
                )}
                <input
                    type="text"
                    placeholder="Title"
                    value={feedbackTitle}
                    onChange={(e) => setFeedbackTitle(e.target.value)}
                    style={{padding: '5px', border: '1px solid #ccc'}}
                />
                <textarea
                    placeholder="Describe your issue or feedback..."
                    value={feedbackText}
                    onChange={(e) => setFeedbackText(e.target.value)}
                    rows="5"
                    style={{padding: '5px', border: '1px solid #ccc'}}
                />
                <div style={{display: 'flex', justifyContent: 'flex-end', gap: '10px'}}>
                    <button className="btn" onClick={() => setFeedbackModalOpen(false)} style={{backgroundColor: '#ccc', color: 'black'}}>Cancel</button>
                    <button className="btn" onClick={submitFeedback} disabled={isSubmittingFeedback} style={{backgroundColor: '#007bff', color: 'white'}}>
                        {isSubmittingFeedback ? 'Sending...' : 'Send Feedback'}
                    </button>
                </div>
            </div>
        </div>
      )}

      <footer style={{ textAlign: 'center', padding: '10px', marginTop: '20px', color: '#888', fontSize: '0.8em' }}>
        Repo Agent UI {process.env.REACT_APP_GIT_SHA ? `(${process.env.REACT_APP_GIT_SHA.substring(0, 7)})` : ''}
      </footer>

    </div>
  );
}

export default App;
