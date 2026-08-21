import type { SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement> & { size?: number }

function IconFrame({ size = 18, children, ...props }: IconProps): React.JSX.Element {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...props}
    >
      {children}
    </svg>
  )
}

export function GorenMark({ size = 30, ...props }: IconProps): React.JSX.Element {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" fill="none" aria-hidden="true" {...props}>
      <path d="M24.6 10.1A10.2 10.2 0 1 0 26 19.3H16.4" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
      <circle cx="24.7" cy="10" r="2.1" fill="#2bb7a9" />
      <circle cx="16.4" cy="19.3" r="1.8" fill="currentColor" />
    </svg>
  )
}

export function PlusIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="M12 5v14M5 12h14" /></IconFrame>
}

export function PanelIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><rect x="3.5" y="4" width="17" height="16" rx="2.5" /><path d="M9 4v16" /></IconFrame>
}

export function SendIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="m5 12 14-7-4.7 14-2.7-5.6L5 12Z" /><path d="m11.6 13.4 3.8-3.8" /></IconFrame>
}

export function StopIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><rect x="7" y="7" width="10" height="10" rx="1.5" fill="currentColor" stroke="none" /></IconFrame>
}

export function SparkIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="m12 3 1.2 4.1L17 9l-3.8 1.9L12 15l-1.2-4.1L7 9l3.8-1.9L12 3Z" /><path d="m18.5 14 .7 2.3 2.3.7-2.3.7-.7 2.3-.7-2.3-2.3-.7 2.3-.7.7-2.3Z" /></IconFrame>
}

export function FolderIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="M3.5 7.5h6l2-2h9v13h-17v-11Z" /></IconFrame>
}

export function ActivityIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="M3 12h4l2-5 4 10 2-5h6" /></IconFrame>
}

export function DatabaseIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><ellipse cx="12" cy="6" rx="7" ry="3" /><path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6" /></IconFrame>
}

export function CloseIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><path d="m6 6 12 12M18 6 6 18" /></IconFrame>
}

export function KeyIcon(props: IconProps): React.JSX.Element {
  return <IconFrame {...props}><circle cx="8.5" cy="15.5" r="4.5" /><path d="m12 12 7.5-7.5M16 8l2 2M18 6l2 2" /></IconFrame>
}
