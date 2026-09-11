export interface ImageInput {
  type: 'image'
  mediaType: string
  data: string
  name: string
}

export interface ImageRef {
  attachmentId: string
  mediaType: string
  name?: string
  width: number
  height: number
  bytes: number
}

export const imageTypes = ['image/png', 'image/jpeg', 'image/webp', 'image/gif']

export function encodeImage(file: File): Promise<ImageInput> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('Unable to read image'))
    reader.onload = () => {
      if (typeof reader.result !== 'string') { reject(new Error('Unable to read image')); return }
      resolve({ type: 'image', mediaType: file.type, data: reader.result.slice(reader.result.indexOf(',') + 1), name: file.name })
    }
    reader.readAsDataURL(file)
  })
}
