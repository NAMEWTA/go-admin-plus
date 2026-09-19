// @vitest-environment happy-dom
import { defineComponent, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { vModalFocus } from '@go-admin-plus/ui'

describe('弹窗键盘焦点', () => {
  it('将焦点留在弹窗，并在卸载后回到触发按钮', async () => {
    const trigger = document.createElement('button')
    document.body.append(trigger)
    trigger.focus()
    const wrapper = mount(defineComponent({
      directives: { modalFocus: vModalFocus },
      template: '<div v-modal-focus><input autofocus><button>确定</button></div>'
    }), { attachTo: document.body })
    await nextTick()
    const first = wrapper.get('input').element
    const last = wrapper.get('button').element
    expect(document.activeElement).toBe(first)
    await wrapper.get('input').trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(last)
    await wrapper.get('button').trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(first)
    wrapper.unmount()
    expect(document.activeElement).toBe(trigger)
    trigger.remove()
  })
})
