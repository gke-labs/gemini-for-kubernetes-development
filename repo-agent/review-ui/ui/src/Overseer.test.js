import React from 'react';
import { formatQueueTimestamp, sortTasksByTimestamp, formatInlineTimestamp, getTimestampDetails } from './Overseer';

jest.mock('./Terminal', () => () => null);

describe('sortTasksByTimestamp', () => {
    test('returns empty array if tasks is falsy or not an array', () => {
        expect(sortTasksByTimestamp(null)).toEqual([]);
        expect(sortTasksByTimestamp(undefined)).toEqual([]);
        expect(sortTasksByTimestamp({})).toEqual([]);
    });

    test('sorts tasks chronologically newest first', () => {
        const tasks = [
            { metadata: { name: 'task-old', creationTimestamp: '2026-09-17T10:00:00Z' } },
            { metadata: { name: 'task-newest', creationTimestamp: '2026-09-17T12:00:00Z' } },
            { metadata: { name: 'task-medium', creationTimestamp: '2026-09-17T11:00:00Z' } }
        ];

        const sorted = sortTasksByTimestamp(tasks);
        expect(sorted.map(t => t.metadata.name)).toEqual(['task-newest', 'task-medium', 'task-old']);
    });

    test('falls back to alphabetical sorting of names for identical timestamps (ascending A-Z)', () => {
        const tasks = [
            { metadata: { name: 'task-b', creationTimestamp: '2026-09-17T11:00:00Z' } },
            { metadata: { name: 'task-c', creationTimestamp: '2026-09-17T11:00:00Z' } },
            { metadata: { name: 'task-a', creationTimestamp: '2026-09-17T11:00:00Z' } }
        ];

        const sorted = sortTasksByTimestamp(tasks);
        expect(sorted.map(t => t.metadata.name)).toEqual(['task-a', 'task-b', 'task-c']);
    });

    test('handles invalid, missing, or null timestamps gracefully', () => {
        const tasks = [
            { metadata: { name: 'task-invalid', creationTimestamp: 'not-a-date' } },
            { metadata: { name: 'task-none' } },
            { metadata: { name: 'task-null', creationTimestamp: null } },
            { metadata: { name: 'task-valid', creationTimestamp: '2026-09-17T11:00:00Z' } }
        ];

        const sorted = sortTasksByTimestamp(tasks);
        // Valid timestamp first (since 2026 > 0)
        // Others have 0 time, sorted alphabetically ascending: 'task-invalid', 'task-none', 'task-null'
        expect(sorted.map(t => t.metadata.name)).toEqual([
            'task-valid',
            'task-invalid',
            'task-none',
            'task-null'
        ]);
    });

    test('does not crash and filters out null or undefined tasks in the list', () => {
        const tasks = [
            null,
            { metadata: { name: 'task-valid', creationTimestamp: '2026-09-17T11:00:00Z' } },
            undefined
        ];
        const sorted = sortTasksByTimestamp(tasks);
        // Assert that the array has exactly length 1 and contains only the valid task
        expect(sorted).toEqual([{ metadata: { name: 'task-valid', creationTimestamp: '2026-09-17T11:00:00Z' } }]);
    });
});

describe('formatQueueTimestamp', () => {
    test('returns "-" for falsy/empty values', () => {
        expect(formatQueueTimestamp(null)).toBe('-');
        expect(formatQueueTimestamp(undefined)).toBe('-');
        expect(formatQueueTimestamp('')).toBe('-');
    });

    test('returns raw string for invalid dates', () => {
        expect(formatQueueTimestamp('not-a-valid-date')).toBe('not-a-valid-date');
    });

    test('formats valid timestamps with exact date and relative time', () => {
        const now = 1750000000000; // Fixed timestamp in ms
        const originalDateNow = Date.now;
        Date.now = jest.fn(() => now);

        const getText = (node) => {
            if (!node || !node.props) return '';
            const c = node.props.children;
            return Array.isArray(c) ? c.join('') : String(c || '');
        };

        try {
            // 1. Future / just now
            const futureDate = new Date(now + 5000).toISOString();
            const futureRes = formatQueueTimestamp(futureDate);
            expect(futureRes).toBeTruthy();
            expect(getText(futureRes.props.children[1])).toBe('(just now)');

            // 2. Seconds ago (< 60s)
            const secondsAgoDate = new Date(now - 25 * 1000).toISOString();
            const secondsRes = formatQueueTimestamp(secondsAgoDate);
            expect(getText(secondsRes.props.children[1])).toBe('(25s ago)');

            // 3. Minutes ago (< 60m)
            const minutesAgoDate = new Date(now - 12 * 60 * 1000).toISOString();
            const minutesRes = formatQueueTimestamp(minutesAgoDate);
            expect(getText(minutesRes.props.children[1])).toBe('(12m ago)');

            // 4. Hours ago (< 24h)
            const hoursAgoDate = new Date(now - (3 * 3600 * 1000 + 45 * 60 * 1000)).toISOString();
            const hoursRes = formatQueueTimestamp(hoursAgoDate);
            expect(getText(hoursRes.props.children[1])).toBe('(3h 45m ago)');

            // 5. Days ago (>= 24h)
            const daysAgoDate = new Date(now - (2 * 24 * 3600 * 1000 + 5 * 3600 * 1000)).toISOString();
            const daysRes = formatQueueTimestamp(daysAgoDate);
            expect(getText(daysRes.props.children[1])).toBe('(2d 5h ago)');

            // Check that exact localized date string is rendered as first child
            const exactExpected = new Date(daysAgoDate).toLocaleString();
            expect(getText(daysRes.props.children[0])).toBe(exactExpected);
        } finally {
            Date.now = originalDateNow;
        }
    });
});

describe('formatInlineTimestamp', () => {
    test('returns "" for falsy/empty values', () => {
        expect(formatInlineTimestamp(null)).toBe('');
        expect(formatInlineTimestamp(undefined)).toBe('');
        expect(formatInlineTimestamp('')).toBe('');
    });

    test('returns raw string for invalid dates', () => {
        expect(formatInlineTimestamp('not-a-valid-date')).toBe('not-a-valid-date');
    });

    test('formats valid timestamps inline with exact date and relative time', () => {
        const now = 1750000000000; // Fixed timestamp in ms
        const originalDateNow = Date.now;
        Date.now = jest.fn(() => now);

        try {
            // Seconds ago (< 60s)
            const secondsAgoDate = new Date(now - 25 * 1000).toISOString();
            const res = formatInlineTimestamp(secondsAgoDate);
            const exact = new Date(secondsAgoDate).toLocaleString();
            expect(res).toBe(`${exact} (25s ago)`);
        } finally {
            Date.now = originalDateNow;
        }
    });
});

describe('getTimestampDetails', () => {
    test('returns null for falsy/empty values', () => {
        expect(getTimestampDetails(null)).toBeNull();
        expect(getTimestampDetails(undefined)).toBeNull();
        expect(getTimestampDetails('')).toBeNull();
    });

    test('returns null for invalid dates', () => {
        expect(getTimestampDetails('not-a-valid-date')).toBeNull();
    });

    test('returns correct details for valid date', () => {
        const now = 1750000000000;
        const originalDateNow = Date.now;
        Date.now = jest.fn(() => now);

        try {
            const dateStr = new Date(now - 5000).toISOString();
            const details = getTimestampDetails(dateStr);
            expect(details).toEqual({
                exact: new Date(dateStr).toLocaleString(),
                rel: '5s ago'
            });
        } finally {
            Date.now = originalDateNow;
        }
    });
});
