// Copyright 2026 The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import React, { useState, useEffect } from 'react';
import yaml from 'js-yaml';

function AddRepo({ onCancel, onRepoAdded }) {
    const [url, setUrl] = useState('');
    const [name, setName] = useState('');
    const [assignees, setAssignees] = useState('');
    const [reviewMaxActiveSandboxes, setReviewMaxActiveSandboxes] = useState(3);
    const [issueMaxActiveSandboxes, setIssueMaxActiveSandboxes] = useState(6);
    const [devMaxActiveSandboxes, setDevMaxActiveSandboxes] = useState(0);
    const [yamlMode, setYamlMode] = useState(false);
    const [yamlContent, setYamlContent] = useState('');
    const [isLoading, setIsLoading] = useState(false);
    const [error, setError] = useState('');
    
    const [templates, setTemplates] = useState([]);
    const [selectedTemplateId, setSelectedTemplateId] = useState('');

    useEffect(() => {
        setIsLoading(true);
        fetch('/api/templates')
            .then(res => res.json())
            .then(data => {
                if (data.templates && data.templates.length > 0) {
                    setTemplates(data.templates);
                    
                    // Optional: Pre-load the 'default' template content into yamlContent 
                    // so the editor isn't blank, but DO NOT set selectedTemplateId
                    // so the user is forced/able to select one from the dropdown.
                    const defaultTmpl = data.templates.find(t => t.id === 'default');
                    if (defaultTmpl) {
                        setYamlContent(defaultTmpl.content);
                    }
                }
                setIsLoading(false);
            })
            .catch(err => {
                console.error(err);
                setError('Failed to load templates');
                setIsLoading(false);
            });
    }, []);

    const handleTemplateChange = (e) => {
        const id = e.target.value;
        setSelectedTemplateId(id);
        
        if (!id) {
            // "New (Custom)" selected - reset to default template but clear inputs
            const defaultTmpl = templates.find(t => t.id === 'default');
            if (defaultTmpl) {
                // Load default YAML but clear specific fields in our inputs
                setUrl('');
                setName('');
                setReviewMaxActiveSandboxes(3);
                setIssueMaxActiveSandboxes(6);
                setDevMaxActiveSandboxes(0);
                // We use the default template structure, but the inputs are blank
                // The user will fill them, which will update the YAML via updateYamlWithInputs
                setYamlContent(defaultTmpl.content);
            }
            return;
        }

        const tmpl = templates.find(t => t.id === id);
        if (tmpl) {
            // Merge current inputs if we wanted to be fancy, but simpler to just load the template content
            // and maybe re-apply the URL/Name if they are set.
            const updated = updateYamlWithInputs(tmpl.content, url, name, assignees, reviewMaxActiveSandboxes, issueMaxActiveSandboxes, devMaxActiveSandboxes);
            setYamlContent(updated);

            // Also update inputs from template defaults if current inputs are empty
             try {
                const docs = [];
                yaml.loadAll(tmpl.content, (d) => docs.push(d));
                const repoWatch = docs.find(d => d && d.kind === 'RepoWatch') || (docs.length === 1 ? docs[0] : null);
                if (repoWatch) {
                    if (repoWatch.spec && repoWatch.spec.repoURL) {
                        setUrl(repoWatch.spec.repoURL);
                    }
                    if (repoWatch.metadata && repoWatch.metadata.name && repoWatch.metadata.name !== 'change-name') {
                        setName(repoWatch.metadata.name);
                    }
                    if (repoWatch.spec && repoWatch.spec.review && repoWatch.spec.review.assignees) {
                        setAssignees(repoWatch.spec.review.assignees.join(', '));
                    }
                    if (repoWatch.spec && repoWatch.spec.review && repoWatch.spec.review.maxActiveSandboxes) {
                        setReviewMaxActiveSandboxes(repoWatch.spec.review.maxActiveSandboxes);
                    } else {
                        setReviewMaxActiveSandboxes(3);
                    }
                    if (repoWatch.spec && repoWatch.spec.issue && repoWatch.spec.issue.maxActiveSandboxes) {
                        setIssueMaxActiveSandboxes(repoWatch.spec.issue.maxActiveSandboxes);
                    } else {
                        setIssueMaxActiveSandboxes(6);
                    }
                    if (repoWatch.spec && repoWatch.spec.dev && repoWatch.spec.dev.maxActiveSandboxes) {
                        setDevMaxActiveSandboxes(repoWatch.spec.dev.maxActiveSandboxes);
                    } else {
                        setDevMaxActiveSandboxes(0);
                    }
                }
            } catch (e) {
                // ignore
            }
        }
    };

    const updateYamlWithInputs = (currentContent, currentUrl, currentName, currentAssignees, currentReviewMax, currentIssueMax, currentDevMax) => {
        try {
            const docs = [];
            yaml.loadAll(currentContent, function (doc) {
                docs.push(doc);
            });

            if (docs.length === 0) return currentContent;

            // Find RepoWatch
            let repoWatchDoc = docs.find(d => d && d.kind === 'RepoWatch');
            
            // If no RepoWatch found, fallback to first doc if only one exists
            if (!repoWatchDoc && docs.length === 1) {
                 repoWatchDoc = docs[0];
            }

            if (repoWatchDoc) {
                if (!repoWatchDoc.spec) repoWatchDoc.spec = {};
                if (!repoWatchDoc.metadata) repoWatchDoc.metadata = {};

                repoWatchDoc.spec.repoURL = currentUrl.trim();
                
                let finalName = currentName.trim();
                // Derive name from URL if not provided
                if (!finalName && currentUrl.trim()) {
                    try {
                        const urlParts = new URL(currentUrl.trim()).pathname.split('/');
                        if (urlParts.length >= 3) {
                            finalName = urlParts[2].replace(".git", ""); 
                        }
                    } catch (e) {
                        // ignore
                    }
                }
                repoWatchDoc.metadata.name = finalName;

                if (currentAssignees && currentAssignees.trim()) {
                    if (!repoWatchDoc.spec.review) repoWatchDoc.spec.review = {};
                    repoWatchDoc.spec.review.assignees = currentAssignees.split(',').map(s => s.trim()).filter(s => s !== '');
                } else if (repoWatchDoc.spec.review) {
                    delete repoWatchDoc.spec.review.assignees;
                }

                // Update Review Max Active Sandboxes
                if (currentReviewMax !== undefined && currentReviewMax !== null) {
                    if (!repoWatchDoc.spec.review) repoWatchDoc.spec.review = {};
                    const max = parseInt(currentReviewMax);
                    repoWatchDoc.spec.review.maxActiveSandboxes = max;
                    repoWatchDoc.spec.review.maxSandboxes = max;
                }

                // Update Issue Max Active Sandboxes
                if (currentIssueMax !== undefined && currentIssueMax !== null) {
                    const max = parseInt(currentIssueMax);
                    if (!repoWatchDoc.spec.issue) repoWatchDoc.spec.issue = { 
                        // Default fields if creating from scratch, though usually template handles this
                        maxActiveSandboxes: max,
                        maxSandboxes: max
                    };
                    else {
                        repoWatchDoc.spec.issue.maxActiveSandboxes = max;
                        repoWatchDoc.spec.issue.maxSandboxes = max;
                    }
                }

                // Update Dev Max Active Sandboxes
                if (currentDevMax !== undefined && currentDevMax !== null) {
                    const max = parseInt(currentDevMax);
                    if (!repoWatchDoc.spec.dev) repoWatchDoc.spec.dev = { 
                        maxActiveSandboxes: max,
                        maxSandboxes: max
                    };
                    else {
                        repoWatchDoc.spec.dev.maxActiveSandboxes = max;
                        repoWatchDoc.spec.dev.maxSandboxes = max;
                    }
                }
            }

            // Dump all back to string joined by ---
            return docs.map(d => yaml.dump(d)).join('\n---\n');
        } catch (e) {
            console.error("Error updating YAML", e);
            return currentContent;
        }
    };

    const handleSwitchToYaml = () => {
        // Update YAML with current inputs before switching
        const updatedYaml = updateYamlWithInputs(yamlContent, url, name, assignees, reviewMaxActiveSandboxes, issueMaxActiveSandboxes, devMaxActiveSandboxes);
        setYamlContent(updatedYaml);
        setYamlMode(true);
    };

    const handleYamlChange = (e) => {
        const newYaml = e.target.value;
        setYamlContent(newYaml);
        try {
            // We only try to parse the RepoWatch to sync back inputs
            const docs = [];
            yaml.loadAll(newYaml, (d) => docs.push(d));
            
            const repoWatch = docs.find(d => d && d.kind === 'RepoWatch') || (docs.length === 1 ? docs[0] : null);

            if (repoWatch) {
                if (repoWatch.spec && repoWatch.spec.repoURL) {
                    setUrl(repoWatch.spec.repoURL);
                }
                if (repoWatch.metadata && repoWatch.metadata.name) {
                    setName(repoWatch.metadata.name);
                }
                if (repoWatch.spec && repoWatch.spec.review && repoWatch.spec.review.assignees) {
                    setAssignees(repoWatch.spec.review.assignees.join(', '));
                } else {
                    setAssignees('');
                }
                if (repoWatch.spec && repoWatch.spec.review && repoWatch.spec.review.maxActiveSandboxes) {
                    setReviewMaxActiveSandboxes(repoWatch.spec.review.maxActiveSandboxes);
                }
                if (repoWatch.spec && repoWatch.spec.issue && repoWatch.spec.issue.maxActiveSandboxes) {
                    setIssueMaxActiveSandboxes(repoWatch.spec.issue.maxActiveSandboxes);
                }
                if (repoWatch.spec && repoWatch.spec.dev && repoWatch.spec.dev.maxActiveSandboxes) {
                    setDevMaxActiveSandboxes(repoWatch.spec.dev.maxActiveSandboxes);
                }
            }
        } catch (e) {
            // Ignore parsing errors while typing
        }
    };

    const handleSubmit = async (e) => {
        e.preventDefault();
        setError('');
        setIsLoading(true);

        let finalYaml = yamlContent;

        if (!yamlMode) {
            finalYaml = updateYamlWithInputs(yamlContent, url, name, assignees, reviewMaxActiveSandboxes, issueMaxActiveSandboxes, devMaxActiveSandboxes);
        }

        // Common Validation
        try {
             // Basic validation on the raw text without full multi-doc parse overhead if we want, 
             // but safer to parse.
             // We just need to check if there is a RepoWatch with a URL.
             const docs = [];
             yaml.loadAll(finalYaml, (d) => docs.push(d));
             
             if (docs.length === 0) throw new Error("YAML is empty");

             const repoWatch = docs.find(d => d && d.kind === 'RepoWatch') || (docs.length === 1 ? docs[0] : null);
             
             if (!repoWatch) throw new Error("No RepoWatch resource found in YAML");

             const repoUrl = repoWatch.spec?.repoURL || '';
             const repoName = repoWatch.metadata?.name || '';

            if (!repoUrl.trim()) {
                setError('Repository URL is required.');
                setIsLoading(false);
                return;
            }
            if (!repoUrl.startsWith('https://github.com/')) {
                setError('Repository URL must start with https://github.com/');
                setIsLoading(false);
                return;
            }
            if (!repoName.trim()) {
                 setError('Repository Name is required (or could not be derived from URL).');
                 setIsLoading(false);
                 return;
            }
            
        } catch (e) {
            setError('Invalid YAML content: ' + e.message);
            setIsLoading(false);
            return;
        }

        const payload = { yaml: finalYaml };

        fetch('/api/repos', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(payload)
        })
        .then(async (res) => {
            if (!res.ok) {
                const data = await res.json();
                throw new Error(data.error || 'Failed to add repository');
            }
            return res;
        })
        .then(() => {
            setIsLoading(false);
            onRepoAdded();
        })
        .catch(err => {
            console.error(err);
            const hint = " This often happens for private or restricted repositories if you haven't provided a manual GitHub PAT in 'Settings'.";
            setError(err.message + hint);
            setIsLoading(false);
        });
    };

    return (
        <div className="add-repo-container">
            <h2>Watch New Repository</h2>
            <p>Enter the full GitHub URL of the repository you want to watch for PRs and Issues.</p>
            
            {error && <div className="message error">{error}</div>}

            <form onSubmit={handleSubmit} className="add-repo-form">
                {!yamlMode && (
                    <>
                        <div className="form-group">
                            <label htmlFor="templateSelect">Repository:</label>
                            <select 
                                id="templateSelect"
                                value={selectedTemplateId}
                                onChange={handleTemplateChange}
                                disabled={isLoading}
                            >
                                <option value="">New (Custom)</option>
                                {templates.filter(t => t.id !== 'default').map(t => (
                                    <option key={t.id} value={t.id}>{t.name}</option>
                                ))}
                            </select>
                            <small>Choose a configuration template or start fresh.</small>
                        </div>

                        <div className="form-group">
                            <label htmlFor="repoUrl">Repository URL:</label>
                            <input
                                type="text"
                                id="repoUrl"
                                value={url}
                                onChange={(e) => setUrl(e.target.value)}
                                placeholder="https://github.com/owner/repo"
                                disabled={isLoading}
                            />
                        </div>

                        <div className="form-group">
                            <label htmlFor="repoName">Name (Optional):</label>
                            <input
                                type="text"
                                id="repoName"
                                value={name}
                                onChange={(e) => setName(e.target.value)}
                                placeholder="Custom name for this watch"
                                disabled={isLoading}
                            />
                            <small>Leave blank to use the repository name.</small>
                        </div>

                        <div className="form-group">
                            <label htmlFor="assignees">Assignees (Optional):</label>
                            <input
                                type="text"
                                id="assignees"
                                value={assignees}
                                onChange={(e) => setAssignees(e.target.value)}
                                placeholder="e.g. username1, username2"
                                disabled={isLoading}
                            />
                            <small>Only watch PRs assigned to these users. Leave blank to watch all PRs.</small>
                        </div>

                        <div className="form-group">
                            <label htmlFor="reviewMax">Review Sandbox Max Count: {reviewMaxActiveSandboxes}</label>
                            <input
                                type="range"
                                id="reviewMax"
                                min="0"
                                max="15"
                                value={reviewMaxActiveSandboxes}
                                onChange={(e) => setReviewMaxActiveSandboxes(parseInt(e.target.value))}
                                disabled={isLoading}
                                style={{width: '100%'}}
                            />
                            <small>Maximum number of concurrent review sandboxes.</small>
                        </div>

                        <div className="form-group">
                            <label htmlFor="issueMax">Issue Sandbox Max Count: {issueMaxActiveSandboxes}</label>
                            <input
                                type="range"
                                id="issueMax"
                                min="0"
                                max="15"
                                value={issueMaxActiveSandboxes}
                                onChange={(e) => setIssueMaxActiveSandboxes(parseInt(e.target.value))}
                                disabled={isLoading}
                                style={{width: '100%'}}
                            />
                            <small>Maximum number of concurrent issue sandboxes.</small>
                        </div>

                        <div className="form-group">
                            <label htmlFor="devMax">Dev Sandbox Max Count: {devMaxActiveSandboxes}</label>
                            <input
                                type="range"
                                id="devMax"
                                min="0"
                                max="15"
                                value={devMaxActiveSandboxes}
                                onChange={(e) => setDevMaxActiveSandboxes(parseInt(e.target.value))}
                                disabled={isLoading}
                                style={{width: '100%'}}
                            />
                            <small>Maximum number of concurrent dev sandboxes.</small>
                        </div>
                    </>
                )}

                {yamlMode && (
                    <div className="form-group">
                        <label htmlFor="repoYaml">RepoWatch YAML:</label>
                        <textarea
                            id="repoYaml"
                            value={yamlContent}
                            onChange={handleYamlChange}
                            className="yaml-editor"
                            rows={15}
                            disabled={isLoading}
                            style={{fontFamily: 'monospace', width: '100%', whiteSpace: 'pre'}}
                        />
                    </div>
                )}

                <div className="form-actions">
                    <button type="submit" className="btn btn-submit" disabled={isLoading}>
                        {isLoading ? 'Adding...' : 'Start Watching'}
                    </button>
                    
                    {!yamlMode ? (
                        <button type="button" className="btn" onClick={handleSwitchToYaml} disabled={isLoading}>
                            Advanced YAML
                        </button>
                    ) : (
                        <button type="button" className="btn" onClick={() => setYamlMode(false)} disabled={isLoading}>
                            Simple Mode
                        </button>
                    )}

                    <button type="button" className="btn" onClick={onCancel} disabled={isLoading}>
                        Cancel
                    </button>
                </div>
            </form>
        </div>
    );
}

export default AddRepo;
