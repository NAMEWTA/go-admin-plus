import type { Directive } from 'vue'

const focusableSelector = 'button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href], [tabindex="0"]'
const cleanup = new WeakMap<HTMLElement, () => void>()

/** 管理弹窗的初始焦点、Tab 循环与关闭后的焦点恢复；Escape 交由所属页面处理。 */
export const vModalFocus: Directive<HTMLElement> = {
  mounted(element) {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null
    const controls = () => [...element.querySelectorAll<HTMLElement>(focusableSelector)]
      .filter(control => !control.closest('[hidden], [inert]') && getComputedStyle(control).visibility !== 'hidden')
    element.tabIndex = -1
    const focusFirst = () => (element.querySelector<HTMLElement>('[autofocus]') ?? controls()[0] ?? element).focus()
    queueMicrotask(() => { if (element.isConnected) focusFirst() })
    const trap = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return
      const candidates = controls()
      const current = document.activeElement
      if (!candidates.length) { event.preventDefault(); element.focus(); return }
      if (event.shiftKey && (current === candidates[0] || current === element)) {
        event.preventDefault(); candidates.at(-1)?.focus()
      } else if (!event.shiftKey && (current === candidates.at(-1) || current === element)) {
        event.preventDefault(); candidates[0]?.focus()
      }
    }
    element.addEventListener('keydown', trap)
    cleanup.set(element, () => {
      element.removeEventListener('keydown', trap)
      if (previous?.isConnected) previous.focus()
    })
  },
  unmounted(element) { cleanup.get(element)?.(); cleanup.delete(element) }
}
