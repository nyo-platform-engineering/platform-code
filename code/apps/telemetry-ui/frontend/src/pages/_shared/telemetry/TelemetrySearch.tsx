import { useId, type ReactNode } from 'react'
import { Controller, useFieldArray, useForm, useWatch } from 'react-hook-form'
import { LuPlus, LuX } from 'react-icons/lu'
import { Button } from '../../../components/Button'
import { SearchableSelect } from '../../../components/SearchableSelect'
import { AttributeKeySelect } from './AttributeKeySelect'
import { validateSearch } from './search-validation'
import {
  ATTRIBUTE_OPERATORS,
  encodeAttributes,
  needsValue,
  readAttributes,
  validateAttributeKey,
  validateJSONPath,
  validateAttributeValue,
  type AttributeCondition,
} from './attribute-filters'
import type { TelemetryMode } from './types'

type SearchFields = { query: string; conditions: AttributeCondition[] }
const inputClass =
  'h-9 w-full min-w-0 rounded border border-border bg-canvas px-2.5 font-mono text-xs text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent aria-invalid:border-danger'

export function TelemetrySearch({
  value,
  attributes,
  mode,
  search,
  onSubmit,
  controls,
}: {
  controls: ReactNode
  value: string
  attributes: string[]
  search: URLSearchParams
  mode: TelemetryMode
  onSubmit: (query: string, attributes: string[]) => void
}) {
  const hintId = useId()
  const initial = readAttributes(attributes)
  const {
    register,
    control,
    handleSubmit,
    reset,
    setValue,
    getValues,
    formState: { errors, isValid, isDirty },
  } = useForm<SearchFields>({
    defaultValues: {
      query: value,
      conditions: initial.conditions.map((item) => ({ ...item, path: item.path ?? '' })),
    },
    mode: 'onChange',
  })
  const { fields, append, remove } = useFieldArray({ control, name: 'conditions' })
  const conditions = useWatch({ control, name: 'conditions' })
  const scopeOptions = [
    { value: 'resource', label: 'Resource' },
    ...(mode === 'logs' ? [{ value: 'body', label: 'JSON body' }] : []),
    { value: mode === 'logs' ? 'log' : 'span', label: mode === 'logs' ? 'Log' : 'Span' },
  ]
  const invalidScope = conditions.some(
    (item) => !scopeOptions.some((option) => option.value === item.scope),
  )
  return (
    <form
      role="search"
      className="grid gap-3"
      noValidate
      onSubmit={handleSubmit((fields) => {
        if (!initial.error && !invalidScope)
          onSubmit(fields.query.trim(), encodeAttributes(fields.conditions))
      })}
    >
      <div className="grid min-w-0 gap-2 min-[700px]:grid-cols-[minmax(0,1fr)_auto]">
        <label className="grid gap-2 text-xs font-semibold text-muted">
          <span className="sr-only">Search {mode}</span>
          <input
            {...register('query', { validate: validateSearch })}
            type="search"
            aria-describedby={hintId}
            aria-invalid={Boolean(errors.query)}
            className="h-11 w-full min-w-0 rounded border border-border bg-canvas px-3 text-sm font-normal text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent aria-invalid:border-danger"
            placeholder={
              mode === 'logs'
                ? 'Message, service or trace ID (optional)'
                : 'Operation, service or trace ID (optional)'
            }
          />
        </label>
        <div className="flex items-center gap-2">
          {' '}
          <Button
            type="submit"
            className="h-11 border-accent bg-accent px-4 font-semibold text-accent-text hover:bg-accent-strong"
            disabled={!isValid || Boolean(initial.error) || invalidScope}
          >
            Run search
          </Button>
          <Button
            onClick={() => {
              reset({ query: '', conditions: [] })
              onSubmit('', [])
            }}
          >
            Clear search
          </Button>
        </div>
      </div>
      {errors.query && (
        <p role="alert" className="text-xs text-danger">
          {errors.query.message}
        </p>
      )}
      <section aria-label="Filters" className="grid min-w-0 gap-3">
        {controls}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Button
            className="flex items-center gap-1.5"
            disabled={fields.length >= 8}
            onClick={() =>
              append({ scope: mode === 'logs' ? 'log' : 'span', key: '', op: 'eq', value: '' })
            }
          >
            <LuPlus aria-hidden />
            Add condition
          </Button>
          <span className="mr-auto text-[11px] text-muted">
            {isDirty
              ? 'Changes not applied'
              : fields.length
                ? `${fields.length} conditions · match all`
                : 'Add an attribute or JSON filter'}
          </span>
        </div>
        <details className="min-w-0 text-[11px] text-muted">
          <summary className="w-fit cursor-pointer rounded border border-accent/40 bg-accent-soft px-2 py-1 font-medium text-accent-strong hover:border-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent">
            Search help
          </summary>
          <p id={hintId} className="mt-2 text-[11px] leading-relaxed text-muted">
            {errors.query?.message ??
              'Exact values are case-sensitive; contains ignores case. Missing keys only match Missing. Search is limited to the selected time range.'}
            {mode === 'traces' && ' Span conditions match request/server spans.'}
            {
              ' JSON paths use dots and zero-based array indexes. Invalid JSON is excluded, including from Missing.'
            }
          </p>
        </details>
        {fields.map((item, index) => {
          const isBody = conditions[index]?.scope === 'body'
          const op = conditions[index]?.op ?? 'eq'
          const conditionErrors = errors.conditions?.[index]
          return (
            <fieldset
              key={item.id}
              className="grid grid-cols-2 items-end gap-2 rounded border border-border bg-canvas/40 p-3 min-[1200px]:grid-cols-[120px_minmax(240px,2fr)_140px_minmax(160px,1fr)_32px]"
            >
              <legend className="sr-only">Condition {index + 1}</legend>
              <Controller
                control={control}
                name={`conditions.${index}.scope`}
                render={({ field }) => (
                  <SearchableSelect
                    label={`Scope ${index + 1}`}
                    options={scopeOptions}
                    values={[field.value]}
                    onChange={(values) => {
                      field.onChange(values[0])
                      setValue(`conditions.${index}.path`, '', { shouldValidate: true })
                      setValue(`conditions.${index}.key`, getValues(`conditions.${index}.key`), {
                        shouldValidate: true,
                      })
                    }}
                  />
                )}
              />
              {isBody ? (
                <label className="grid gap-1.5 text-[11px] font-semibold text-muted">
                  {isBody ? 'JSON path' : 'Attribute key'} {index + 1}
                  <input
                    {...register(`conditions.${index}.key`, {
                      validate: (value) =>
                        validateAttributeKey(value) === true && isBody
                          ? validateJSONPath(value)
                          : validateAttributeKey(value),
                    })}
                    className={inputClass}
                    aria-invalid={Boolean(conditionErrors?.key)}
                    placeholder={isBody ? 'user.id or items.0.price' : 'http.request.method'}
                  />
                </label>
              ) : (
                <Controller
                  control={control}
                  name={`conditions.${index}.key`}
                  rules={{ validate: validateAttributeKey }}
                  render={({ field }) => (
                    <AttributeKeySelect
                      key={`${conditions[index]?.scope}:${search.toString()}`}
                      label={`Attribute key ${index + 1}`}
                      value={field.value}
                      onChange={field.onChange}
                      mode={mode}
                      scope={conditions[index]?.scope ?? 'resource'}
                      search={search}
                    />
                  )}
                />
              )}
              <Controller
                control={control}
                name={`conditions.${index}.op`}
                render={({ field }) => (
                  <SearchableSelect
                    label={`Operator ${index + 1}`}
                    options={ATTRIBUTE_OPERATORS}
                    values={[field.value]}
                    onChange={(values) => {
                      field.onChange(values[0])
                      setValue(
                        `conditions.${index}.value`,
                        needsValue(values[0]) ? getValues(`conditions.${index}.value`) : '',
                        { shouldValidate: true, shouldDirty: true },
                      )
                    }}
                  />
                )}
              />
              {needsValue(op) ? (
                <label className="grid gap-1.5 text-[11px] font-semibold text-muted">
                  Value {index + 1}
                  <input
                    {...register(`conditions.${index}.value`, {
                      validate: (value) =>
                        validateAttributeValue(value, getValues(`conditions.${index}.op`)),
                    })}
                    className={inputClass}
                    aria-invalid={Boolean(conditionErrors?.value)}
                    placeholder={['gt', 'gte', 'lt', 'lte'].includes(op) ? '500' : 'Exact value'}
                  />
                </label>
              ) : (
                <span className="py-2 text-[11px] text-muted">No value needed</span>
              )}
              <Button
                className="w-7 px-0"
                aria-label={`Remove condition ${index + 1}`}
                onClick={() => remove(index)}
              >
                <LuX aria-hidden className="mx-auto" />
              </Button>
              {!isBody && (
                <details
                  className="col-span-full"
                  open={conditions[index]?.path ? true : undefined}
                >
                  <summary className="cursor-pointer text-[11px] text-muted">
                    Extract a JSON field (optional)
                  </summary>
                  <label className="mt-2 grid gap-1.5 text-[11px] text-muted">
                    JSON path {index + 1} (optional, when the attribute contains JSON)
                    <input
                      {...register(`conditions.${index}.path`, {
                        validate: (value) => validateJSONPath(value ?? ''),
                      })}
                      className={inputClass}
                      placeholder="user.id or items.0.price"
                      aria-invalid={Boolean(conditionErrors?.path)}
                    />
                  </label>
                </details>
              )}
              {(conditionErrors?.key || conditionErrors?.value || conditionErrors?.path) && (
                <p className="col-span-full text-[11px] text-danger" role="alert">
                  {conditionErrors.key?.message ??
                    conditionErrors.value?.message ??
                    conditionErrors.path?.message}
                </p>
              )}
            </fieldset>
          )
        })}
        {(initial.error || invalidScope) && (
          <p role="alert" className="text-xs text-danger">
            {initial.error ??
              'An attribute scope does not match this page. Choose the correct scope or clear the conditions.'}
          </p>
        )}
      </section>
    </form>
  )
}
