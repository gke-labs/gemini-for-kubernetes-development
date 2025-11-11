import React, { useState } from 'react';

function DeleteRepo({ repo, onRepoDeleted }) {
    const [isConfirming, setIsConfirming] = useState(false);
    const [confirmationName, setConfirmationName] = useState('');
    const [isDeleting, setIsDeleting] = useState(false);
    const [error, setError] = useState(null);

    const handleDeleteClick = () => {
        setIsConfirming(true);
    };

    const handleCancelClick = () => {
        setIsConfirming(false);
        setConfirmationName('');
        setError(null);
    };

    const handleConfirmDelete = async () => {
        if (confirmationName !== repo.name) {
            return;
        }

        setIsDeleting(true);
        setError(null);

        try {
            const response = await fetch(`/api/repowatch/${repo.namespace}/${repo.name}`, {
                method: 'DELETE',
            });

            if (!response.ok) {
                const data = await response.json();
                throw new Error(data.error || 'Failed to delete repository');
            }

            if (onRepoDeleted) {
                onRepoDeleted(repo.name);
            }
        } catch (err) {
            setError(err.message);
            setIsDeleting(false);
        }
    };

    if (!isConfirming) {
        return (
            <button className="btn btn-delete" onClick={handleDeleteClick} style={{ marginLeft: 'auto' }}>
                Delete Repo
            </button>
        );
    }

    return (
        <div className="delete-repo-confirmation" style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: '10px' }}>
            {error && <span style={{ color: 'red' }}>{error}</span>}
            <span>Type <strong>{repo.name}</strong> to confirm:</span>
            <input
                type="text"
                value={confirmationName}
                onChange={(e) => setConfirmationName(e.target.value)}
                placeholder={repo.name}
                style={{ padding: '5px', borderRadius: '4px', border: '1px solid #ccc' }}
            />
            <button
                className="btn btn-delete"
                onClick={handleConfirmDelete}
                disabled={confirmationName !== repo.name || isDeleting}
            >
                {isDeleting ? 'Deleting...' : 'Confirm Delete'}
            </button>
            <button className="btn" onClick={handleCancelClick} disabled={isDeleting}>
                Cancel
            </button>
        </div>
    );
}

export default DeleteRepo;
