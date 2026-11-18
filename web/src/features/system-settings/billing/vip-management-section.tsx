/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useFieldArray, useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { SettingsCard } from '../components/settings-card'
import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import type { BillingSettings, UpdateOptionResponse } from '../types'

const tierSettingsSchema = z.object({
  enabled: z.boolean(),
  window_days: z.number().int().positive(),
  tiers: z
    .array(
      z.object({
        level: z.number().int(),
        key: z.string(),
        name: z.string(),
        min_amount: z.number(),
      })
    )
    .length(6),
})
const emailGroupSchema = z.object({
  enabled: z.boolean(),
  apply_on_bind: z.boolean(),
  rules: z
    .array(z.object({ suffix: z.string(), group: z.string() }))
    .nullable(),
})
const ratiosSchema = z.record(z.string(), z.number())

type VIPForm = {
  enabled: boolean
  windowDays: number
  tiers: Array<
    z.infer<typeof tierSettingsSchema>['tiers'][number] & { ratio: number }
  >
  emailEnabled: boolean
  applyOnBind: boolean
  rules: Array<{ suffix: string; group: string }>
}

function parseOption<T>(raw: string, schema: z.ZodType<T>): T | undefined {
  try {
    return schema.safeParse(JSON.parse(raw)).data
  } catch {
    return undefined
  }
}

export function VipManagementSection({
  settings,
}: {
  settings: BillingSettings
}) {
  const { t } = useTranslation()
  const vip = parseOption(settings.VipTierSetting, tierSettingsSchema)
  const email = parseOption(settings.EmailGroupRules, emailGroupSchema)
  const ratios = parseOption(settings.TopupGroupRatio, ratiosSchema)
  const groups = parseOption(settings.GroupRatio, ratiosSchema)

  if (!vip || !email || !ratios) {
    return <p role='alert'>{t('VIP settings could not be loaded')}</p>
  }

  const values: VIPForm = {
    enabled: vip.enabled,
    windowDays: vip.window_days,
    tiers: vip.tiers.map((tier) => ({ ...tier, ratio: ratios[tier.key] ?? 1 })),
    emailEnabled: email.enabled,
    applyOnBind: email.apply_on_bind,
    rules: email.rules ?? [],
  }
  const discountGroups = Object.keys(groups ?? {}).filter(
    (group) => !['default', 'vip', 'svip'].includes(group)
  )
  return (
    <VipManagementForm defaultValues={values} discountGroups={discountGroups} />
  )
}

function VipManagementForm({
  defaultValues,
  discountGroups,
}: {
  defaultValues: VIPForm
  discountGroups: string[]
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const form = useForm({ defaultValues })
  const rules = useFieldArray({ control: form.control, name: 'rules' })

  const save = useMutation({
    mutationFn: async (values: VIPForm) => {
      const response = await api.patch<UpdateOptionResponse>(
        '/api/option/vip',
        {
          vip_tier_setting: {
            enabled: values.enabled,
            window_days: values.windowDays,
            tiers: values.tiers.map(({ level, key, name, min_amount }) => ({
              level,
              key,
              name,
              min_amount,
            })),
          },
          email_group_rules: {
            enabled: values.emailEnabled,
            apply_on_bind: values.applyOnBind,
            rules: values.rules,
          },
          topup_ratios: Object.fromEntries(
            values.tiers.map(({ key, ratio }) => [key, ratio])
          ),
        }
      )
      return requireServerSuccess(response.data)
    },
    onSuccess: (_, values) => {
      form.reset(values)
      void queryClient.invalidateQueries({ queryKey: ['system-options'] })
      void queryClient.invalidateQueries({ queryKey: ['status'] })
      try {
        window.localStorage.removeItem('status')
      } catch {
        // Storage may be disabled.
      }
      toast.success(t('Setting updated successfully'))
    },
    onError: (error) => handleServerError(error, t('Failed to update setting')),
  })

  const submit = form.handleSubmit(
    (values) => save.mutate(values),
    () => toast.error(t('Invalid parameters'))
  )
  return (
    <SettingsForm onSubmit={submit} autoComplete='off'>
      <SettingsPageFormActions
        onSave={submit}
        isSaving={save.isPending}
        isSaveDisabled={!form.formState.isDirty}
      />
      <fieldset disabled={save.isPending} className='space-y-4'>
        <SettingsCard title={t('VIP Membership')}>
          <div className='grid gap-4'>
            <div className='flex items-center gap-3'>
              <Switch
                id='vip-enabled'
                checked={form.watch('enabled')}
                onCheckedChange={(checked) =>
                  form.setValue('enabled', checked, { shouldDirty: true })
                }
              />
              <Label htmlFor='vip-enabled'>
                {t('Enable automatic VIP tiers')}
              </Label>
            </div>
            <div className='grid max-w-xs gap-2'>
              <Label htmlFor='vip-window'>{t('Growth window (days)')}</Label>
              <Input
                id='vip-window'
                type='number'
                min={1}
                required
                {...form.register('windowDays', {
                  valueAsNumber: true,
                  min: 1,
                  required: true,
                })}
              />
            </div>
            <div className='grid gap-3'>
              {defaultValues.tiers.map(({ key, level }, index) => (
                <fieldset
                  key={key}
                  className='grid gap-3 rounded-md border p-3 sm:grid-cols-3'
                >
                  <legend className='px-1 text-sm font-medium'>{key}</legend>
                  <div className='grid gap-2'>
                    <Label htmlFor={key + '-name'}>{t('Tier name')}</Label>
                    <Input
                      id={key + '-name'}
                      required
                      {...form.register(`tiers.${index}.name`, {
                        required: true,
                      })}
                    />
                  </div>
                  <div className='grid gap-2'>
                    <Label htmlFor={key + '-threshold'}>
                      {t('Growth threshold')}
                    </Label>
                    <Input
                      id={key + '-threshold'}
                      type='number'
                      min={0}
                      step='any'
                      required
                      readOnly={level === 0}
                      {...form.register(`tiers.${index}.min_amount`, {
                        valueAsNumber: true,
                        required: true,
                        min: 0,
                      })}
                    />
                  </div>
                  <div className='grid gap-2'>
                    <Label htmlFor={key + '-ratio'}>{t('Top-up Ratio')}</Label>
                    <Input
                      id={key + '-ratio'}
                      type='number'
                      min={0}
                      step='any'
                      required
                      {...form.register(`tiers.${index}.ratio`, {
                        valueAsNumber: true,
                        required: true,
                        min: 0,
                      })}
                    />
                  </div>
                </fieldset>
              ))}
            </div>
          </div>
        </SettingsCard>
        <SettingsCard title={t('Email Domain Discounts')}>
          <div className='grid gap-4'>
            <div className='flex items-center gap-3'>
              <Switch
                id='vip-email-enabled'
                checked={form.watch('emailEnabled')}
                onCheckedChange={(checked) =>
                  form.setValue('emailEnabled', checked, { shouldDirty: true })
                }
              />
              <Label htmlFor='vip-email-enabled'>
                {t('Enable email domain discounts')}
              </Label>
            </div>
            <div className='flex items-center gap-3'>
              <Switch
                id='vip-email-bind'
                checked={form.watch('applyOnBind')}
                onCheckedChange={(checked) =>
                  form.setValue('applyOnBind', checked, { shouldDirty: true })
                }
              />
              <Label htmlFor='vip-email-bind'>
                {t('Apply on email binding')}
              </Label>
            </div>
            <p className='text-muted-foreground text-sm'>
              {t('VIP discounts follow the user’s current VIP level.')}
            </p>
            {rules.fields.map((rule, index) => (
              <div
                key={rule.id}
                className='grid items-end gap-3 sm:grid-cols-[1fr_1fr_auto]'
              >
                <div className='grid gap-2'>
                  <Label htmlFor={rule.id + '-suffix'}>
                    {t('Domain suffix')}
                  </Label>
                  <Input
                    id={rule.id + '-suffix'}
                    placeholder='edu.cn'
                    required
                    {...form.register(`rules.${index}.suffix`, {
                      required: true,
                    })}
                  />
                </div>
                <div className='grid gap-2'>
                  <Label htmlFor={rule.id + '-group'}>
                    {t('Discount group')}
                  </Label>
                  <NativeSelect
                    id={rule.id + '-group'}
                    className='w-full'
                    required
                    {...form.register(`rules.${index}.group`, {
                      required: true,
                    })}
                  >
                    <NativeSelectOption value=''>
                      {t('Select a group')}
                    </NativeSelectOption>
                    {rule.group && !discountGroups.includes(rule.group) && (
                      <NativeSelectOption value={rule.group}>
                        {rule.group}
                      </NativeSelectOption>
                    )}
                    {discountGroups.map((group) => (
                      <NativeSelectOption key={group} value={group}>
                        {group}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                <Button
                  type='button'
                  variant='outline'
                  onClick={() => rules.remove(index)}
                >
                  {t('Remove')}
                </Button>
              </div>
            ))}
            <Button
              type='button'
              variant='outline'
              className='justify-self-start'
              onClick={() => rules.append({ suffix: '', group: '' })}
            >
              {t('Add Rule')}
            </Button>
          </div>
        </SettingsCard>
      </fieldset>
    </SettingsForm>
  )
}
