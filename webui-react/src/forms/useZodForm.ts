import { zodResolver } from '@hookform/resolvers/zod';
import { useForm, type FieldValues, type UseFormProps, type UseFormReturn } from 'react-hook-form';
import type { z } from 'zod';

// Thin wrapper that wires React Hook Form to a Zod schema (v5.41.0).
// Unifies validation across modals so each one stops rolling its own
// useState + manual `if (!name) setErr(...)` plumbing. Adoption is
// incremental — the modals migrate one at a time. New forms should
// use this helper from day one.
//
// Example:
//   const schema = z.object({
//     name: vmNameSchema,
//     vcpus: vcpuSchema,
//   });
//   const form = useZodForm(schema, { defaultValues: { name: '', vcpus: 2 } });
//   const onSubmit = form.handleSubmit((values) => create.mutate(values));
//
//   <input {...form.register('name')} />
//   {form.formState.errors.name && (
//     <span className="form-error">{form.formState.errors.name.message}</span>
//   )}

export function useZodForm<T extends z.ZodTypeAny>(
  schema: T,
  options: Omit<UseFormProps<z.infer<T>>, 'resolver'> = {},
): UseFormReturn<z.infer<T>> {
  return useForm<z.infer<T>>({
    resolver: zodResolver(schema),
    mode: 'onBlur',
    ...options,
  } as UseFormProps<FieldValues> as UseFormProps<z.infer<T>>);
}
