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
            const response = await fetch(`/api/repos/${repo.name}`, {
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
                Delete Repowatch
            </button>
        );
    }

    return (
        <div className="delete-repo-confirmation" style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: '10px' }}>
            {error && <span style={{ color: 'var(--text-danger)' }}>{error}</span>}
            <span>Type <strong>{repo.name}</strong> to confirm:</span>
            <input
                type="text"
                value={confirmationName}
                onChange={(e) => setConfirmationName(e.target.value)}
                placeholder={repo.name}
                style={{ padding: '5px', borderRadius: '4px', border: '1px solid var(--border-color)' }}
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
