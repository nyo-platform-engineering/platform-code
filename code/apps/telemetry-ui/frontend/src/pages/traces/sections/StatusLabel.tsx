export function StatusLabel({ status }: { status: string }) {
  return (
    <span
      className="text-[10px] text-muted data-[error=true]:text-danger"
      data-error={status === 'Error'}
    >
      {status === 'Error' ? 'Error' : status === 'Ok' ? 'OK' : 'Unset'}
    </span>
  )
}
