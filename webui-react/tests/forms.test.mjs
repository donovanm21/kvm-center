import assert from 'node:assert/strict';
import test from 'node:test';
import { pathToFileURL } from 'node:url';
import { join } from 'node:path';
import { z } from 'zod';
import { zodResolver } from '@hookform/resolvers/zod';
import { createFormControl } from 'react-hook-form';

if (!process.env.KVMC_FORMS_TEST_BUILD) throw new Error('Run npm run test:forms to compile the actual form sources first');
const {
  createVMSchema, snapshotFormSchema, tokenFormSchema,
  vmNameSchema, memMBSchema, vcpuSchema, diskGBSchema,
} = await import(pathToFileURL(join(process.env.KVMC_FORMS_TEST_BUILD, 'src/forms/schemas.js')));
const options = { criteriaMode: 'all', fields: {}, shouldUseNativeValidation: false };
const resolve = (schema, values, resolverOptions) => zodResolver(schema, undefined, resolverOptions)(values, {}, options);

test('invalid submissions resolve field errors instead of rejecting with ZodError', async () => {
  const schema = z.object({ name: vmNameSchema, memory: memMBSchema });
  for (const mode of ['async', 'sync']) {
    const result = await resolve(schema, { name: '', memory: 127 }, { mode });
    assert.deepEqual(result.values, {});
    assert.equal(result.errors.name.message, 'VM name is required');
    assert.equal(result.errors.memory.message, 'Memory must be at least 128 MB');
    assert.equal(result.errors.name.types.too_small, 'VM name is required');
    assert.equal(result.errors.name.types.invalid_format, 'Use letters, digits, dot, underscore, hyphen only');
  }
});

test('default first-error mode and nested field paths remain usable by RHF', async () => {
  const schema = z.object({ guests: z.array(z.object({ name: vmNameSchema })) });
  const result = await zodResolver(schema)({ guests: [{ name: '' }] }, {}, { ...options, criteriaMode: 'firstError' });
  assert.deepEqual(result.values, {});
  assert.equal(result.errors.guests[0].name.message, 'VM name is required');
  assert.equal(result.errors.guests[0].name.types, undefined);
});

test('snapshot validation preserves exact boundaries without adding trimming', async () => {
  for (const name of ['snapshot_2026-10.16', 'a'.repeat(64)]) {
    assert.deepEqual(await resolve(snapshotFormSchema, { name }), { values: { name }, errors: {} });
  }
  for (const name of ['', 'a'.repeat(65), 'has spaces', ' padded ']) {
    assert.ok((await resolve(snapshotFormSchema, { name })).errors.name);
  }
});

test('numeric inputs preserve whole-number bounds and reject unconverted select strings', () => {
  for (const [schema, min, max] of [[memMBSchema, 128, 4194304], [vcpuSchema, 1, 128], [diskGBSchema, 1, 65536]]) {
    for (const value of [min, max]) assert.equal(schema.safeParse(value).success, true);
    for (const value of [min - 1, max + 1, min + 0.5, `${min}`, NaN, Infinity]) {
      assert.equal(schema.safeParse(value).success, false);
    }
  }
  assert.equal(tokenFormSchema.safeParse({ name: 'label', days: '30' }).success, false);
});

test('actual token schema trims before required/length checks and preserves expiry bounds', async () => {
  assert.deepEqual(await resolve(tokenFormSchema, { name: '  laptop-kvmctl  ', days: 30 }), {
    values: { name: 'laptop-kvmctl', days: 30 }, errors: {},
  });
  assert.equal((await resolve(tokenFormSchema, { name: '  ', days: 30 })).errors.name.message, 'Token name is required');
  assert.equal(tokenFormSchema.safeParse({ name: `  ${'a'.repeat(64)}  `, days: 1 }).success, true);
  assert.equal(tokenFormSchema.safeParse({ name: 'a'.repeat(65), days: 30 }).success, false);
  assert.equal(tokenFormSchema.safeParse({ name: 'label', days: 3650 }).success, true);
  for (const days of [0, 3651, 1.5]) assert.equal(tokenFormSchema.safeParse({ name: 'label', days }).success, false);
});

test('actual VM schema keeps ISO/import refinements and PXE does not require a disk path', async () => {
  const base = { name: 'new-vm', memory: 2048, vcpus: 2, diskSize: 20, source: 'iso', isoPath: '', importDisk: '' };
  const missingISO = createVMSchema.safeParse(base);
  assert.equal(missingISO.success, false);
  assert.deepEqual(missingISO.error.issues[0].path, ['isoPath']);
  assert.equal(missingISO.error.issues[0].message, 'Pick an ISO or switch install source.');
  assert.equal(createVMSchema.safeParse({ ...base, isoPath: '/fixture.iso' }).success, true);
  assert.equal(createVMSchema.safeParse({ ...base, source: 'pxe' }).success, true);
  const imported = { ...base, source: 'import', importDisk: ' ' };
  assert.equal((await resolve(createVMSchema, imported)).errors.importDisk.message, 'Provide the path to the existing disk image.');
  assert.equal(createVMSchema.safeParse({ ...imported, importDisk: '/fixture.qcow2', diskSize: 1 }).success, true);
});

test('resolver passes transformed/defaulted output while leaving registered input untouched', async () => {
  const schema = z.object({ count: z.string().transform(Number), label: z.string().default('default label') });
  const input = { count: '3' };
  assert.deepEqual(await resolve(schema, input), { values: { count: 3, label: 'default label' }, errors: {} });
  assert.deepEqual(input, { count: '3' });
});

test('React Hook Form rejects invalid edits, clears errors after corrections, and resets defaults', async () => {
  const form = createFormControl({ resolver: zodResolver(tokenFormSchema), defaultValues: { name: '', days: 30 }, mode: 'onBlur' });
  const unsubscribe = form.subscribe({ formState: { errors: true, isDirty: true }, callback: () => {} });
  form.register('name');
  form.register('days', { valueAsNumber: true });
  const submitted = [];
  const rejected = [];
  const submit = form.handleSubmit((values) => submitted.push(values), (errors) => rejected.push(errors));
  try {
    for (const name of ['', '  ', 'x'.repeat(65)]) {
      form.setValue('name', name);
      await submit();
      assert.ok(form.getFieldState('name').error);
    }
    assert.equal(rejected.length, 3);
    assert.deepEqual(submitted, []);
    form.setValue('name', '  corrected  ', { shouldDirty: true });
    form.setValue('days', 90);
    await submit();
    assert.deepEqual(submitted, [{ name: 'corrected', days: 90 }]);
    assert.equal(form.getFieldState('name').invalid, false);
    assert.equal(form.getValues('name'), '  corrected  ');
    // Same successful-submit reset used by MyTokensModal: preserve selected days.
    form.reset({ name: '', days: form.getValues('days') });
    assert.deepEqual(form.getValues(), { name: '', days: 90 });
    assert.equal(form.getFieldState('name').isDirty, false);
    assert.equal(form.getFieldState('name').invalid, false);
    await submit();
    assert.equal(submitted.length, 1);
    assert.equal(rejected.length, 4);
    // Same close/reopen reset used by MyTokensModal.
    form.reset({ name: '', days: 30 });
    assert.deepEqual(form.getValues(), { name: '', days: 30 });
    assert.equal(form.getFieldState('name').invalid, false);
    form.setValue('name', 'again');
    await submit();
    assert.deepEqual(submitted[1], { name: 'again', days: 30 });
  } finally {
    unsubscribe();
  }
});
