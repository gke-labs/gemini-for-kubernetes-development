import React, { useState } from 'react';

function AddRepo({ onRepoAdded }) {
    const [name, setName] = useState('');
    const [repoURL, setRepoURL] = useState('');
    const [error, setError] = useState(null);
    const [isSubmitting, setIsSubmitting] = useState(false);

    const handleSubmit = async (e) => {
        e.preventDefault();
        setError(null);
        setIsSubmitting(true);

        try {
            const response = await fetch('/api/repowatch', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                // Hardcoding namespace to 'default' for now for single tenant usage
                // TODO: when multi-tenant support is added this should use the tenant namespace
                body: JSON.stringify({ name, repoURL, namespace: 'default' }),
            });

            if (!response.ok) {
                const data = await response.json();
                throw new Error(data.error || 'Failed to add repository');
            }

            // Success!
            setName('');
            setRepoURL('');
            if (onRepoAdded) {
                onRepoAdded();
            }
            alert('Repository added successfully!');
        } catch (err) {
            setError(err.message);
        } finally {
            setIsSubmitting(false);
        }
    };

    return (
        <div className="pr-card"> {/* Re-using pr-card for consistent styling */}
            <h3>Add New Repository</h3>
            {error && <div style={{ color: 'red', marginBottom: '10px' }}>{error}</div>}
            <form onSubmit={handleSubmit} className="review-form">
                <div style={{ marginBottom: '15px' }}>
                    <label htmlFor="name" style={{ display: 'block', marginBottom: '5px', fontWeight: 'bold' }}>Name:</label>
                    <input
                        type="text"
                        id="name"
                        value={name}
                        onChange={(e) => setName(e.target.value)}
                        required
                        placeholder="e.g., kubernetes"
                        style={{ width: '100%', padding: '8px', borderRadius: '4px', border: '1px solid #ccc' }}
                    />
                </div>
                <div style={{ marginBottom: '15px' }}>
                    <label htmlFor="repoURL" style={{ display: 'block', marginBottom: '5px', fontWeight: 'bold' }}>Repository URL:</label>
                    <input
                        type="url"
                        id="repoURL"
                        value={repoURL}
                        onChange={(e) => setRepoURL(e.target.value)}
                        required
                        placeholder="https://github.com/owner/repo"
                        style={{ width: '100%', padding: '8px', borderRadius: '4px', border: '1px solid #ccc' }}
                    />
                </div>
                <button type="submit" className="btn btn-submit" disabled={isSubmitting}>
                    {isSubmitting ? 'Adding...' : 'Add Repository'}
                </button>
            </form>
        </div>
    );
}

export default AddRepo;
