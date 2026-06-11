/**
 * Pure unit tests for the helper functions extracted from src/index.js.
 *
 * These run with plain `vitest` — no Cloudflare-specific pool required.
 * The helpers are re-exported via test/helpers.js to avoid importing the
 * full Worker module (which references `crypto.getRandomValues` at module
 * level in some runtimes).
 */

import { describe, it, expect } from 'vitest';
import {
  timingSafeEqual,
  mintID,
  validateEnqueueBody,
  validateIDs,
} from './helpers.js';

// ---------------------------------------------------------------------------
// timingSafeEqual
// ---------------------------------------------------------------------------

describe('timingSafeEqual', () => {
  it('returns true for identical strings', () => {
    expect(timingSafeEqual('abc', 'abc')).toBe(true);
  });

  it('returns false when strings differ', () => {
    expect(timingSafeEqual('abc', 'abd')).toBe(false);
  });

  it('returns false when lengths differ', () => {
    expect(timingSafeEqual('abc', 'abcd')).toBe(false);
    expect(timingSafeEqual('abcd', 'abc')).toBe(false);
  });

  it('returns false when one is empty', () => {
    expect(timingSafeEqual('', 'abc')).toBe(false);
    expect(timingSafeEqual('abc', '')).toBe(false);
  });

  it('returns true for two empty strings', () => {
    expect(timingSafeEqual('', '')).toBe(true);
  });

  it('is case-sensitive', () => {
    expect(timingSafeEqual('ABC', 'abc')).toBe(false);
  });

  it('handles unicode / multi-byte strings', () => {
    expect(timingSafeEqual('café', 'café')).toBe(true);
    expect(timingSafeEqual('café', 'cafe')).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// mintID
// ---------------------------------------------------------------------------

describe('mintID', () => {
  const ID_RE = /^qi-[0-9a-f]{8}$/;

  it('matches the qi-[0-9a-f]{8} contract', () => {
    for (let i = 0; i < 20; i++) {
      expect(mintID()).toMatch(ID_RE);
    }
  });

  it('returns different ids on successive calls (probabilistic)', () => {
    const ids = new Set();
    for (let i = 0; i < 50; i++) ids.add(mintID());
    // Birthday collision for 32-bit ids is negligible at 50 draws.
    expect(ids.size).toBe(50);
  });
});

// ---------------------------------------------------------------------------
// validateEnqueueBody
// ---------------------------------------------------------------------------

describe('validateEnqueueBody', () => {
  it('accepts a minimal valid body', () => {
    expect(validateEnqueueBody({ text: 'buy milk' })).toBeNull();
  });

  it('accepts all optional fields', () => {
    expect(validateEnqueueBody({
      text: 'hello',
      project: 'work/qi',
      due: '2026-06-15',
      scheduled: '2026-06-14',
      source: 'ios-shortcut',
    })).toBeNull();
  });

  it('rejects non-object input', () => {
    expect(validateEnqueueBody(null)).not.toBeNull();
    expect(validateEnqueueBody('string')).not.toBeNull();
    expect(validateEnqueueBody([1, 2])).not.toBeNull();
  });

  it('rejects missing text', () => {
    expect(validateEnqueueBody({})).not.toBeNull();
  });

  it('rejects empty text after trim', () => {
    expect(validateEnqueueBody({ text: '   ' })).not.toBeNull();
  });

  it('rejects text with control chars — newline', () => {
    expect(validateEnqueueBody({ text: 'foo\nbar' })).not.toBeNull();
  });

  it('rejects text with control chars — tab (0x09)', () => {
    expect(validateEnqueueBody({ text: 'foo\tbar' })).not.toBeNull();
  });

  it('rejects text with DEL (0x7f)', () => {
    expect(validateEnqueueBody({ text: 'foo\x7fbar' })).not.toBeNull();
  });

  it('rejects text with carriage return (0x0d)', () => {
    expect(validateEnqueueBody({ text: 'foo\rbar' })).not.toBeNull();
  });

  it('accepts text with printable ASCII and unicode', () => {
    expect(validateEnqueueBody({ text: 'Fix café build 🎉' })).toBeNull();
  });

  it('rejects bad project charset — space', () => {
    expect(validateEnqueueBody({ text: 'x', project: 'my project' })).not.toBeNull();
  });

  it('rejects bad project charset — @', () => {
    expect(validateEnqueueBody({ text: 'x', project: 'proj@foo' })).not.toBeNull();
  });

  it('accepts project with letters, digits, underscore, hyphen, slash', () => {
    expect(validateEnqueueBody({ text: 'x', project: 'work/qi_2-B' })).toBeNull();
  });

  it('rejects project and client together', () => {
    expect(validateEnqueueBody({ text: 'x', project: 'foo', client: 'bar' })).not.toBeNull();
  });

  it('accepts client without project', () => {
    expect(validateEnqueueBody({ text: 'x', client: 'acme' })).toBeNull();
  });

  it('rejects bad due date format', () => {
    expect(validateEnqueueBody({ text: 'x', due: '06-15-2026' })).not.toBeNull();
    expect(validateEnqueueBody({ text: 'x', due: '2026/06/15' })).not.toBeNull();
    expect(validateEnqueueBody({ text: 'x', due: 'tomorrow' })).not.toBeNull();
  });

  it('accepts valid due date', () => {
    expect(validateEnqueueBody({ text: 'x', due: '2026-06-15' })).toBeNull();
  });

  it('rejects bad scheduled date format', () => {
    expect(validateEnqueueBody({ text: 'x', scheduled: '2026-6-1' })).not.toBeNull();
  });

  it('accepts valid scheduled date', () => {
    expect(validateEnqueueBody({ text: 'x', scheduled: '2026-06-01' })).toBeNull();
  });

  it('rejects unknown fields', () => {
    expect(validateEnqueueBody({ text: 'x', priority: 'high' })).not.toBeNull();
  });

  it('accepts null optional fields (explicit null = omitted)', () => {
    expect(validateEnqueueBody({ text: 'x', project: null, client: null, due: null, scheduled: null, source: null })).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateIDs
// ---------------------------------------------------------------------------

describe('validateIDs', () => {
  it('accepts a valid array of ids', () => {
    expect(validateIDs(['qi-aabbccdd', 'qi-11223344'])).toBeNull();
  });

  it('rejects null', () => {
    expect(validateIDs(null)).not.toBeNull();
  });

  it('rejects a non-array', () => {
    expect(validateIDs('qi-aabbccdd')).not.toBeNull();
  });

  it('rejects empty array', () => {
    expect(validateIDs([])).not.toBeNull();
  });

  it('rejects array containing empty string', () => {
    expect(validateIDs(['qi-aabb', ''])).not.toBeNull();
  });

  it('rejects array containing non-string', () => {
    expect(validateIDs([42])).not.toBeNull();
  });

  it('rejects oversized array (> 500)', () => {
    const ids = Array.from({ length: 501 }, (_, i) => `qi-${i.toString(16).padStart(8, '0')}`);
    expect(validateIDs(ids)).not.toBeNull();
  });

  it('accepts exactly 500 ids', () => {
    const ids = Array.from({ length: 500 }, (_, i) => `qi-${i.toString(16).padStart(8, '0')}`);
    expect(validateIDs(ids)).toBeNull();
  });
});
