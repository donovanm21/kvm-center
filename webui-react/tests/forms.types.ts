import { z } from 'zod';
import { useZodForm } from '../src/forms/useZodForm';
import { snapshotFormSchema, tokenFormSchema } from '../src/forms/schemas';

// Compile-only regression: never called at runtime. Input is what fields store;
// output is the parsed value passed to handleSubmit, including defaults.
export function useFormTypeRegression() {
  const schema = z.object({
    count: z.string().transform(Number),
    label: z.string().default('default label'),
  });
  const form = useZodForm(schema, { defaultValues: { count: '2' } });
  const input: string = form.watch('count');
  const optionalLabel: string | undefined = form.watch('label');
  form.setValue('count', '3');
  form.reset({ count: '4' });
  // @ts-expect-error Input remains a string before parsing.
  form.setValue('count', 3);
  // @ts-expect-error Reset receives input, not transformed output.
  form.reset({ count: 4 });
  // @ts-expect-error Register only accepts input field paths.
  form.register('missing');
  form.handleSubmit((output) => {
    output.count.toFixed();
    output.label.toUpperCase();
    // @ts-expect-error The submit callback receives numeric output.
    output.count.trim();
  });
  // @ts-expect-error Defaults use input types.
  useZodForm(schema, { defaultValues: { count: 2 } });
  // @ts-expect-error Form schemas must describe object-shaped input fields.
  useZodForm(z.string().transform((name) => ({ name })));
  // @ts-expect-error Parsed form output must be object-shaped too.
  useZodForm(z.object({ count: z.number() }).transform((value) => value.count));

  const snapshot = useZodForm(snapshotFormSchema, { defaultValues: { name: '' } });
  snapshot.handleSubmit((value) => value.name.toUpperCase());
  const token = useZodForm(tokenFormSchema, { defaultValues: { name: '', days: 30 } });
  token.register('days', { valueAsNumber: true });
  // @ts-expect-error The app converts the select value to a number before validation.
  token.setValue('days', '30');
  return { input, optionalLabel };
}
