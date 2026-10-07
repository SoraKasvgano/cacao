<template>
  <div class="container">
    <a-form
      :model="registerState"
      :hideRequiredMark="true"
      name="register"
      class="register-form"
      @finish="onFinish"
    >
      <a-form-item 
        name="username" 
        :rules="[{ required: true, message: $t('register.inputUsername') }]"
      >
        <a-input 
          v-model:value="registerState.username"
          :placeholder="$t('register.username')"
        >
          <template #prefix>
            <UserOutlined class="site-form-item-icon" />
          </template>
        </a-input>
      </a-form-item>

      <a-form-item 
        name="password" 
        :rules="[{ required: true, message: $t('register.inputPassword') }]"
      >
        <a-input 
          type="password" 
          autocomplete="new-password" 
          v-model:value="registerState.password"
          :placeholder="$t('register.password')"
        >
          <template #prefix>
            <LockOutlined class="site-form-item-icon" />
          </template>
        </a-input>
      </a-form-item>

      <a-form-item name="setupToken" :label="$t('register.setupToken')">
        <a-input-password
          v-model:value="registerState.setupToken"
          autocomplete="off"
          :placeholder="$t('register.setupTokenPlaceholder')"
        />
        <div class="setup-help">{{ $t('register.setupTokenHelp') }}</div>
      </a-form-item>

      <a-form-item>
        <a-button type="primary" html-type="submit" class="register-form-button" :loading="submitting">
          {{ $t('register.register') }}
        </a-button>
      </a-form-item>
    </a-form>
  </div>
</template>

<script setup>
import { reactive, ref } from 'vue'
import axios from 'axios'
import { useRouter } from 'vue-router'

const registerState = reactive({
  username: '',
  password: '',
  setupToken: ''
})

const router = useRouter()
const submitting = ref(false)

const onFinish = async (values) => {
  if (submitting.value) return
  submitting.value = true
  try {
    const response = await axios.post('/api/user/register', {
      username: values.username,
      password: values.password,
      setupToken: values.setupToken || undefined
    })
    if (response.data.status === 0) {
      registerState.password = ''
      registerState.setupToken = ''
      router.replace('/')
    }
  } catch {
    // The global API interceptor displays request failures, including rate limits.
  } finally {
    submitting.value = false
  }
}
</script>

<style scoped>
.container {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 100vh;
}

.register-form {
  max-width: 300px;
}
.register-form-button {
  width: 100%;
}
.setup-help {
  margin-top: 8px;
  color: #666;
  font-size: 12px;
}
</style>
