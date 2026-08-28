interface BoundCatalogFailureProps {
  message?: string
  label: string
  retryLabel: string
  disabled: boolean
  onRetry: () => void
}

export function BoundCatalogFailure({ message, label, retryLabel, disabled, onRetry }: BoundCatalogFailureProps): React.JSX.Element {
  return (
    <div className="bound-catalog-failure" role="alert">
      <span>{label}{message === undefined ? '' : `: ${message}`}</span>
      <button type="button" disabled={disabled} onClick={onRetry}>{retryLabel}</button>
    </div>
  )
}
