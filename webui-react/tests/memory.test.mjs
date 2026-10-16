import assert from 'node:assert/strict';
import test from 'node:test';
import { pathToFileURL } from 'node:url';
import { join } from 'node:path';

const { formatMemoryUsage } = await import(pathToFileURL(join(process.env.KVMC_FORMS_TEST_BUILD, 'src/utils/memory.js')));

test('memory ratios retain sub-GiB capacity and consistent units', () => {
  for (const [used, total, expected] of [
    [256, 256, '256 / 256 MiB'],
    [128, 512, '128 / 512 MiB'],
    [0, 256, '0 / 256 MiB'],
    [0.5, 0.5, '0.5 / 0.5 MiB'],
    [512, 1023, '512 / 1023 MiB'],
    [512, 1024, '0.5 / 1.0 GiB'],
    [1024, 1536, '1.0 / 1.5 GiB'],
    [1048576, 2097152, '1024.0 / 2048.0 GiB'],
    [300, 256, '300 / 256 MiB'], // report observed overcommit; do not clamp
  ]) assert.equal(formatMemoryUsage(used, total), expected);
});

test('unknown capacity and invalid usage do not imply measured zero', () => {
  for (const total of [0, -1, undefined, null, NaN, Infinity]) assert.equal(formatMemoryUsage(12, total), '—');
  for (const used of [-1, undefined, null, NaN, Infinity]) {
    assert.equal(formatMemoryUsage(used, 256), '— / 256 MiB');
    assert.equal(formatMemoryUsage(used, 1024), '— / 1.0 GiB');
  }
});
