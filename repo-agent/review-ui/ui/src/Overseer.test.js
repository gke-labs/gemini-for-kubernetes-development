import React from 'react';
import { formatQueueTimestamp } from './Overseer';

jest.mock('./Terminal', () => () => null);

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
