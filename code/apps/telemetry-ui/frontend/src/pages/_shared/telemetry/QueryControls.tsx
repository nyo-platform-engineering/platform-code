import { Controller, useForm } from 'react-hook-form'
import { SearchableSelect } from '../../../components/SearchableSelect'
import { filterValues, type SetFilter } from './filters'
import { SEVERITIES, type TelemetryMode } from './types'
import type { DataSourceOption } from '../../../layouts/MetadataProvider'

type Props = {
  mode: TelemetryMode
  search: URLSearchParams
  services: string[]
  dataSources: DataSourceOption[]
  setFilter: SetFilter
}

export function QueryControls({ mode, search, services, dataSources, setFilter }: Props) {
  const service = filterValues(search, 'service')
  const serviceNames = [...new Set([...service, ...services])].filter(Boolean)

  const { control } = useForm({
    values: {
      service,
      dataSource: [search.get(mode === 'traces' ? 'traceSource' : 'logSource') ?? ''],
      severity: filterValues(search, 'severity'),
      status: filterValues(search, 'status'),
      minDurationMs: [search.get('minDurationMs') ?? ''],
    },
  })

  return (
    <div className="grid gap-4">
      <div
        className={`grid grid-cols-2 gap-x-2 gap-y-3 ${mode === 'traces' ? 'min-[700px]:grid-cols-4' : 'min-[700px]:grid-cols-3'}`}
      >
        <Controller
          control={control}
          name="dataSource"
          render={({ field }) => (
            <SearchableSelect
              label="Datasource"
              options={dataSources.map((source) => ({
                value: source.id,
                label: `${source.id} · ${source.address}`,
              }))}
              values={field.value}
              placeholder="Waiting for datasource"
              minSelected={1}
              onChange={(values) => {
                if (!values[0]) return
                field.onChange(values)
                setFilter(mode === 'traces' ? 'traceSource' : 'logSource', values[0])
              }}
            />
          )}
        />
        <Controller
          control={control}
          name="service"
          render={({ field }) => (
            <SearchableSelect
              label="Service"
              multiple
              minSelected={1}
              options={serviceNames.map((name) => ({ value: name, label: name }))}
              values={field.value}
              placeholder="Waiting for services"
              onChange={(values) => {
                if (!values.length) return
                field.onChange(values)
                setFilter('service', values)
              }}
            />
          )}
        />
        {mode === 'logs' ? (
          <Controller
            control={control}
            name="severity"
            render={({ field }) => (
              <SearchableSelect
                label="Severity"
                multiple
                maxSelected={7}
                options={SEVERITIES.map((level) => ({
                  value: level,
                  label: level[0].toUpperCase() + level.slice(1),
                }))}
                values={field.value}
                placeholder="All severities"
                onChange={(values) => {
                  field.onChange(values)
                  setFilter('severity', values)
                }}
              />
            )}
          />
        ) : (
          <>
            <Controller
              control={control}
              name="status"
              render={({ field }) => (
                <SearchableSelect
                  label="Status"
                  multiple
                  maxSelected={2}
                  options={[
                    { value: 'error', label: 'Error' },
                    { value: 'ok', label: 'Not error' },
                  ]}
                  values={field.value}
                  placeholder="All statuses"
                  onChange={(values) => {
                    field.onChange(values)
                    setFilter('status', values)
                  }}
                />
              )}
            />
            <Controller
              control={control}
              name="minDurationMs"
              render={({ field }) => (
                <SearchableSelect
                  label="Duration"
                  options={[
                    { value: '', label: 'Any duration' },
                    { value: '100', label: '≥ 100 ms' },
                    { value: '500', label: '≥ 500 ms' },
                    { value: '1000', label: '≥ 1 second' },
                  ]}
                  values={field.value}
                  onChange={(values) => {
                    field.onChange(values)
                    setFilter('minDurationMs', values)
                  }}
                />
              )}
            />
          </>
        )}
      </div>
    </div>
  )
}
